package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/constants"
	"github.com/trevex/chalkos/pkg/internal/golden"
	"github.com/trevex/chalkos/pkg/pki"
)

// refusingHandler refuses Reboot for want of a role and implements no other method.
type refusingHandler struct {
	nodev1connect.UnimplementedNodeServiceHandler
}

func (refusingHandler) Reboot(context.Context, *nodev1.RebootRequest) (*nodev1.RebootResponse, error) {
	return nil, connect.NewError(connect.CodePermissionDenied, "/chalkos.node.v1.NodeService/Reboot needs the operator role")
}

// TestDescribe words the errors of calls on one line each: a dial that timed out without connect's
// code and net/http's request line, with the hint to check chalkd and the address; the node's own
// refusals with its message and the role the credentials grant or the release hint; a call that
// ran out of time with its own words. The code stays readable.
func TestDescribe(t *testing.T) {
	hints := Hints{Credentials: "the client file reader.json", Role: pki.RoleReader, Client: "chalkctl", Release: "0.6.0"}
	var lines []string
	describe := func(name string, err error, code connect.Code) {
		t.Helper()
		d := Describe(err, hints)
		if connect.CodeOf(d) != code {
			t.Errorf("%s: code %v, want %v", name, connect.CodeOf(d), code)
		}
		lines = append(lines, name+": "+d.Error())
	}

	unreachable, err := Dial("10.255.255.1", Options{Verify: AnyCertificate(), Source: "--address"})
	if err != nil {
		t.Fatal(err)
	}
	defer unreachable.Close()
	unreachable.netDial = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.255.255.1"), Port: 50000}, Err: os.ErrDeadlineExceeded}
	}
	_, err = unreachable.Status(context.Background(), &nodev1.StatusRequest{})
	describe("dial timeout", err, connect.CodeOf(err))
	describe("wrapped", fmt.Errorf("upgrade: %w", err), connect.CodeOf(err))

	addr := serveNode(t, refusingHandler{})
	c, err := Dial(addr, Options{Verify: AnyCertificate()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Reboot(context.Background(), &nodev1.RebootRequest{})
	describe("permission", err, connect.CodePermissionDenied)
	_, err = c.Status(context.Background(), &nodev1.StatusRequest{})
	describe("unimplemented", err, connect.CodeUnimplemented)

	describe("no answer", connect.NewError(connect.CodeDeadlineExceeded, "chalkd on n1:50000 did not answer within 2m").WithCause(context.DeadlineExceeded), connect.CodeDeadlineExceeded)
	golden.Check(t, "testdata/describe.golden", strings.ReplaceAll(strings.Join(lines, "\n"), addr, "ADDR")+"\n")

	other := errors.New("no inventory")
	if got := Describe(other, hints); got != other {
		t.Errorf("another error became %v", got)
	}
}

// Every reason a change rule may name is one of the node API's, by the same name.
func TestRuleReasonsAreTheAPIs(t *testing.T) {
	for _, name := range constants.RuleReasons {
		r := ReasonNamed(name)
		if r == nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED || ReasonName(r) != name {
			t.Errorf("rule reason %s is no ErrorReason of the node API", name)
		}
	}
}
