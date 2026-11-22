// Command chalkos-registry serves pkg/registry on a port of every address and its reads on a Unix
// socket, and is the catalog diagnostic of the unit that runs it.
//
// It runs as a system user of its own and reads the node certificate and the OS CAs once, from
// the systemd credentials its unit loads from chalkd's files, which only root may read. When
// chalkd replaces them, the unit restarts it.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"

	"github.com/trevex/chalkos/pkg/registry"
)

const usage = `usage: chalkos-registry serve [--port PORT] [--socket SOCKET] [--storage DIR] [--certificate FILE] [--ca FILE]
       chalkos-registry catalog SOCKET

serve serves the registry on PORT of every address of both families, 5000 unless given, over
TLS with the node certificate's chain and key in FILE. It verifies client certificates against
the OS CAs of --ca and keeps the images in DIR, /var/lib/chalkos-registry unless given. The
files default to the credentials node.pem and ca.crt of $CREDENTIALS_DIRECTORY. With --socket it
also serves reads without a certificate on the Unix socket SOCKET, which its group may connect
to. catalog prints the repositories of the registry whose socket is SOCKET.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-registry: "+err.Error())
		if errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, usage)
		}
		os.Exit(1)
	}
}

// errUsage is a command line chalkos-registry does not take; main prints the usage after it.
var errUsage = errors.New("unknown command line")

// command runs the command line args.
func command(ctx context.Context, args []string, stdout io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "catalog":
		return get(ctx, stdout, args[1], "/v2/_catalog")
	case len(args) > 0 && args[0] == "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		port := fs.Int("port", 5000, "")
		socket := fs.String("socket", "", "")
		c := registry.Config{}
		fs.StringVar(&c.Storage, "storage", "/var/lib/chalkos-registry", "")
		fs.StringVar(&c.Certificate, "certificate", "", "")
		fs.StringVar(&c.CA, "ca", "", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return errUsage
		}
		if c.Certificate == "" || c.CA == "" {
			dir := os.Getenv("CREDENTIALS_DIRECTORY")
			if dir == "" {
				return errors.New("no credentials directory; give --certificate and --ca")
			}
			c.Certificate = cmp.Or(c.Certificate, filepath.Join(dir, "node.pem"))
			c.CA = cmp.Or(c.CA, filepath.Join(dir, "ca.crt"))
		}
		registry.QuietLogs()
		// A failure to read the credentials ends the process with one line, which the unit's
		// restart repeats no more often than its restart delay.
		s, err := registry.New(ctx, c)
		if err != nil {
			return err
		}
		l, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(*port)))
		if err != nil {
			return err
		}
		var local net.Listener
		if *socket != "" {
			if local, err = listenLocal(*socket); err != nil {
				l.Close()
				return err
			}
		}
		return run(ctx, l, local, s)
	}
	return errUsage
}

// listenLocal listens on the Unix socket path, which its owner and group may connect to. A
// socket a stopped registry left behind is removed first.
func listenLocal(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// run serves s on l, and its reads on local unless it is nil, until ctx ends. The journal gets
// distribution's warnings and errors, and no line for a request that the client got wrong, such
// as a pull of a missing image, nor for a failed TLS handshake.
func run(ctx context.Context, l, local net.Listener, s *registry.Server) error {
	// The unit is Type=notify: systemctl start and restart return once the port is open, so the
	// effects ordered after the registry's find it answering. Outside systemd it sends nothing.
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		l.Close()
		if local != nil {
			local.Close()
		}
		return err
	}
	srv := &http.Server{
		Handler:           s,
		TLSConfig:         s.TLSConfig(),
		ReadHeaderTimeout: 30 * time.Second,
		// A connection without a request closes after two minutes.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    registry.ErrorLog(os.Stderr),
	}
	servers := []*http.Server{srv}
	errs := make(chan error, 2)
	go func() { errs <- srv.ServeTLS(l, "", "") }()
	if local != nil {
		lsrv := &http.Server{
			Handler:           s.Local(),
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			ErrorLog:          registry.ErrorLog(os.Stderr),
		}
		servers = append(servers, lsrv)
		go func() { errs <- lsrv.Serve(local) }()
	}
	// Either server failing stops the other, as the end of ctx stops both.
	running := len(servers)
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
		running--
	}
	for _, srv := range servers {
		srv.Shutdown(context.Background())
	}
	for ; running > 0; running-- {
		<-errs
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// get prints path of the registry whose socket is socket. The diagnostic that calls it sees
// neither the node certificate nor the OS CA, and reads over the socket as a member of its group.
func get(ctx context.Context, w io.Writer, socket, path string) error {
	var d net.Dialer
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", socket)
		}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://registry"+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the registry does not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the registry answers %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
