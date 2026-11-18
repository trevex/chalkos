// Command chalkos-registry serves pkg/registry on a port of every address, and is the catalog
// diagnostic of the unit that runs it.
//
// It runs as a system user of its own and reads the node certificate and the OS CAs once, from
// the systemd credentials its unit loads from chalkd's files, which only root may read. When
// chalkd replaces them, the unit restarts it.
package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
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

const usage = `usage: chalkos-registry serve [--port PORT] [--storage DIR] [--certificate FILE] [--ca FILE]
       chalkos-registry catalog PORT

serve serves the registry on PORT of every address of both families, 5000 unless given, over
TLS with the node certificate's chain and key in FILE. It verifies client certificates against
the OS CAs of --ca and keeps the images in DIR, /var/lib/chalkos-registry unless given. The
files default to the credentials node.pem and ca.crt of $CREDENTIALS_DIRECTORY. catalog prints
the repositories of the registry on PORT of this node.`

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
		return get(ctx, stdout, net.JoinHostPort("127.0.0.1", args[1]), "/v2/_catalog")
	case len(args) > 0 && args[0] == "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		port := fs.Int("port", 5000, "")
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
		return run(ctx, l, s)
	}
	return errUsage
}

// run serves s on l until ctx ends. The journal gets distribution's warnings and errors, and no
// line for a request that the client got wrong, such as a pull of a missing image, nor for a
// failed TLS handshake.
func run(ctx context.Context, l net.Listener, s *registry.Server) error {
	// The unit is Type=notify: systemctl start and restart return once the port is open, so the
	// effects ordered after the registry's find it answering. Outside systemd it sends nothing.
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		l.Close()
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
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	if err := srv.ServeTLS(l, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// get prints path of the registry at addr. It does not verify the registry's certificate: the
// diagnostic that calls it sees no OS CA, and the connection stays on the node.
func get(ctx context.Context, w io.Writer, addr, path string) error {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
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
