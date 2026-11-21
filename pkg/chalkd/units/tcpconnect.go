package units

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// dialer opens a connection, as net.Dialer.DialContext does; tests replace it.
type dialer func(ctx context.Context, network, address string) (net.Conn, error)

// dial is the dialer of a real connect.
var dial dialer = (&net.Dialer{}).DialContext

// timeoutStatus is tcp-connect's exit status when no connection came within its timeout, as
// coreutils' timeout exits.
const timeoutStatus = 124

// TCPConnect opens a TCP connection to address and port with dial, closes it again, and prints
// "connected to <address> port <port>" to stdout. A connect that does not succeed within timeout
// prints why to stderr and fails with exit status 124; one that fails otherwise, such as one that
// is refused, prints why and fails with status 1.
func TCPConnect(ctx context.Context, dial dialer, address, port string, timeout time.Duration, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort(address, port))
	if err == nil {
		_ = conn.Close()
		fmt.Fprintf(stdout, "connected to %s port %s\n", address, port)
		return nil
	}
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "no connection to %s port %s within %d seconds\n", address, port, int(timeout/time.Second))
		return cli.Status(timeoutStatus)
	}
	// The system call's error alone, such as "connection refused": the address and port are said
	// already.
	if se, ok := errors.AsType[*os.SyscallError](err); ok {
		err = se.Err
	}
	fmt.Fprintf(stderr, "no connection to %s port %s: %v\n", address, port, err)
	return cli.Status(1)
}
