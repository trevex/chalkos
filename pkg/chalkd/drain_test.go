package chalkd

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	"github.com/trevex/chalkos/pkg/pki"
)

// stalledRequests are the requests of a client that sends nothing until release is closed, and
// then closes its side. receiving is closed once the call waits for the first request.
type stalledRequests[M any] struct {
	once               sync.Once
	receiving, release chan struct{}
}

func newStalledRequests[M any]() *stalledRequests[M] {
	return &stalledRequests[M]{receiving: make(chan struct{}), release: make(chan struct{})}
}

func (r *stalledRequests[M]) Receive() (M, error) {
	r.once.Do(func() { close(r.receiving) })
	<-r.release
	var none M
	return none, io.EOF
}

// An upgrade whose client sends no header holds no lock, so a delivery goes ahead.
func TestUpgradeWithoutAHeaderHoldsNoLock(t *testing.T) {
	s, _ := configServer(t)
	in := newStalledRequests[*nodev1.UpgradeRequest]()
	done := make(chan error, 1)
	go func() {
		_, err := s.upgrade(context.Background(), in)
		done <- err
	}()
	<-in.receiving
	if _, err := sendConfig(s, nextConfig); err != nil {
		t.Errorf("the delivery during the upgrade's wait for its header: %v", err)
	}
	close(in.release)
	if err := <-done; codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("upgrade = %v, want the missing header refused", err)
	}
}

// An install whose client sends no header holds no lock either.
func TestInstallWithoutAHeaderHoldsNoLock(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	in := newStalledRequests[*nodev1.InstallRequest]()
	done := make(chan error, 1)
	go func() {
		_, err := s.install(context.Background(), in)
		done <- err
	}()
	<-in.receiving
	if !s.mu.TryLock() {
		t.Error("the install holds s.mu while it waits for its header")
	} else {
		s.mu.Unlock()
	}
	close(in.release)
	if err := <-done; codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("install = %v, want the missing header refused", err)
	}
}

// The stream of an upgrade or an install whose client sends nothing ends after the idle bound,
// over HTTP/2 as chalkctl calls.
func TestIdleStreamsEnd(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      nodev1.Mode
		procedure string
	}{
		{"upgrade", normal, nodev1connect.NodeServiceUpgradeProcedure},
		{"install", maintenance, nodev1connect.NodeServiceInstallProcedure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, tc.mode, vda)
			s.streamIdle = 100 * time.Millisecond
			addr := serve(t, s, newCreds(t), nil)
			// The request's body sends nothing until the test ends.
			body, w := io.Pipe()
			t.Cleanup(func() { w.Close() })
			req, err := http.NewRequest(http.MethodPost, "https://"+addr+tc.procedure, body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/connect+proto")
			hc := &http.Client{Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				ForceAttemptHTTP2: true,
			}}
			t.Cleanup(hc.CloseIdleConnections)
			start := time.Now()
			resp, err := hc.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.ProtoMajor != 2 {
				t.Errorf("the call used %s, want HTTP/2", resp.Proto)
			}
			if !strings.Contains(string(got), "deadline_exceeded") || !strings.Contains(string(got), "the client sent nothing for 100ms") {
				t.Errorf("the call ended with %q, want the idle bound named", got)
			}
			if took := time.Since(start); took < s.streamIdle {
				t.Errorf("the call ended after %v, before the idle bound", took)
			}
		})
	}
}

// Reboot and Shutdown wait for a change of the node and then refuse, naming it; forced, they act
// at once. Once the change ended they act and hold the changes' locks until the node goes down.
func TestRebootWaitsForChanges(t *testing.T) {
	s, _ := newTestServer(t, normal, vda)
	var rebooted, poweredOff int
	s.RebootNode = func() {
		rebooted++
		if s.upgrading.TryLock() {
			s.upgrading.Unlock()
			t.Error("the reboot does not hold the changes' locks")
		}
	}
	s.PowerOffNode = func() { poweredOff++ }
	reboot := func(force bool) error {
		_, err := s.Reboot(context.Background(), &nodev1.RebootRequest{Force: force})
		return err
	}
	shutdown := func(force bool) error {
		_, err := s.Shutdown(context.Background(), &nodev1.ShutdownRequest{Force: force})
		return err
	}
	unlock, err := s.lockSlots("an upgrade")
	if err != nil {
		t.Fatal(err)
	}
	testwait.Bubble(t, func(t *testing.T) {
		for _, call := range []func(bool) error{reboot, shutdown} {
			start := time.Now()
			err := call(false)
			if codeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "an upgrade is running") {
				t.Errorf("during an upgrade: %v, want ABORTED naming the upgrade", err)
			}
			if took := time.Since(start); took != changeWait {
				t.Errorf("refused after %v, want after %v", took, changeWait)
			}
		}
	})
	if rebooted != 0 || poweredOff != 0 {
		t.Fatalf("refused calls rebooted %d and powered off %d times", rebooted, poweredOff)
	}
	if err := reboot(true); err != nil || rebooted != 1 {
		t.Errorf("a forced reboot: %v, rebooted %d times", err, rebooted)
	}
	if err := shutdown(true); err != nil || poweredOff != 1 {
		t.Errorf("a forced shutdown: %v, powered off %d times", err, poweredOff)
	}
	unlock()
	if err := reboot(false); err != nil || rebooted != 2 {
		t.Errorf("a reboot once the upgrade ended: %v, rebooted %d times", err, rebooted)
	}
	if !s.upgrading.TryLock() || !s.mu.TryLock() {
		t.Fatal("the reboot keeps the changes' locks once the node went down")
	}
	s.mu.Unlock()
	s.upgrading.Unlock()
}

// blockedDiagnosticStream is the stream of a client that stops reading: Send blocks until
// unblock is closed. sending is closed once a send blocks.
type blockedDiagnosticStream struct {
	once             sync.Once
	sending, unblock chan struct{}
}

func (b *blockedDiagnosticStream) Send(*nodev1.RunDiagnosticResponse) error {
	b.once.Do(func() { close(b.sending) })
	<-b.unblock
	return nil
}

// diagnoseFunc is a function as a diagnostics.Runner.
type diagnoseFunc func(ctx context.Context, r diagnostics.Run, stdout, stderr io.Writer) (diagnostics.Exit, error)

func (f diagnoseFunc) Run(ctx context.Context, r diagnostics.Run, stdout, stderr io.Writer) (diagnostics.Exit, error) {
	return f(ctx, r, stdout, stderr)
}

// A diagnostic's slot is free once its unit ended, while a client that stops reading still
// blocks the send of its output.
func TestDiagnosticSlotFreeOnceTheRunEnds(t *testing.T) {
	s, _ := newTestServer(t, normal, vda)
	writeContract(t, s, contract.DiagnosticsFile, testDiagnostics)
	stream := &blockedDiagnosticStream{sending: make(chan struct{}), unblock: make(chan struct{})}
	// The unit prints, and ends while the copy of its output blocks, as the sandbox's run returns
	// once the unit stopped and its pipes' wait passed.
	s.Diagnose = diagnoseFunc(func(_ context.Context, _ diagnostics.Run, stdout, _ io.Writer) (diagnostics.Exit, error) {
		go stdout.Write([]byte("output"))
		<-stream.sending
		return diagnostics.Exit{}, nil
	})
	ctx := context.WithValue(context.Background(), roleKey{}, pki.RoleReader)
	done := make(chan error, 1)
	go func() {
		done <- s.runDiagnostic(ctx, &nodev1.RunDiagnosticRequest{Name: "route-get", Arguments: map[string]string{"address": "192.0.2.1"}}, stream)
	}()
	<-stream.sending
	testwait.Eventually(t, "the diagnostic's slot is free", func() bool { return s.diagnosticRuns.Load() == 0 })
	select {
	case err := <-done:
		t.Fatalf("the call ended while a send blocks: %v", err)
	default:
	}
	close(stream.unblock)
	if err := <-done; err != nil {
		t.Errorf("the call ended with %v once the client read again", err)
	}
}
