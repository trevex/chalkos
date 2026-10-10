package chalkd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// TestUpgrade streams an image to the node: the header and the chunks reach the installation,
// which may answer that the node runs the image already, and the node reboots when asked to.
func TestUpgrade(t *testing.T) {
	c := newCreds(t)
	image := bytes.Repeat([]byte("store hash uki "), 200000)
	header := &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{
		Version: "0.2.0", ImageId: "chalkos", Cluster: "lab", Role: "worker", Architecture: "x86-64", RootHash: []byte{1, 2},
		Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{4}}, HashTree: &nodev1.ImagePart{Size: 2, Sha256: []byte{5}}, Uki: &nodev1.ImagePart{Size: 3, Sha256: []byte{6}},
	}}
	for _, tc := range []struct {
		name       string
		reboot     bool
		result     upgrade.Result
		err        error
		wantReboot bool
		wantCode   connect.Code
	}{
		{"installed", false, upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, nil, false, 0},
		{"installed and rebooting", true, upgrade.Result{Entry: "chalkos_0.2.0+3.efi"}, nil, true, 0},
		{"already installed", true, upgrade.Result{AlreadyInstalled: true}, nil, false, 0},
		{"refused", true, upgrade.Result{}, errors.New("the image's role is controlplane, the node's worker"), false, connect.CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, normal, vda)
			rebooted := make(chan struct{})
			s.RebootNode = func() { close(rebooted) }
			var got []byte
			s.InstallImage = func(_ context.Context, h upgrade.Header, r io.Reader) (upgrade.Result, error) {
				want := upgrade.Header{ImageID: "chalkos", Version: "0.2.0", Cluster: "lab", Role: "worker", RootHash: []byte{1, 2},
					StoreSize: 1, VeritySize: 2, UKISize: 3, StoreSHA256: []byte{4}, VeritySHA256: []byte{5}, UKISHA256: []byte{6}}
				if h.Version != want.Version || h.ImageID != want.ImageID || h.Cluster != want.Cluster || h.Role != want.Role || h.Architecture != "x86-64" ||
					!bytes.Equal(h.RootHash, want.RootHash) || h.StoreSize != 1 || h.VeritySize != 2 || h.UKISize != 3 ||
					!bytes.Equal(h.StoreSHA256, want.StoreSHA256) || !bytes.Equal(h.VeritySHA256, want.VeritySHA256) || !bytes.Equal(h.UKISHA256, want.UKISHA256) {
					t.Errorf("header %+v", h)
				}
				var err error
				got, err = io.ReadAll(r)
				if err != nil {
					t.Error(err)
				}
				return tc.result, tc.err
			}
			addr := serve(t, s, c, c.pool)
			conn := dial(t, addr, c.clients[pki.RoleOperator])
			stream := conn.Upgrade(context.Background())
			h := proto.Clone(header).(*nodev1.UpgradeHeader)
			h.Reboot = tc.reboot
			if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: h}}); err != nil {
				t.Fatal(err)
			}
			for rest := image; len(rest) > 0; {
				n := min(len(rest), 1<<20)
				if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: rest[:n]}}}); err != nil {
					t.Fatal(err)
				}
				rest = rest[n:]
			}
			resp, err := stream.CloseAndReceive()
			if connect.CodeOf(err) != tc.wantCode && !(err == nil && tc.wantCode == 0) {
				t.Fatalf("upgrade = %v, want code %v", err, tc.wantCode)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "the image's role is controlplane") {
					t.Errorf("error %v does not say why", err)
				}
			} else if resp.Msg.Entry != tc.result.Entry || resp.Msg.AlreadyInstalled != tc.result.AlreadyInstalled || resp.Msg.Rebooting != tc.wantReboot {
				t.Errorf("response %+v", resp.Msg)
			}
			if !bytes.Equal(got, image) {
				t.Errorf("the installation received %d bytes, want %d", len(got), len(image))
			}
			select {
			case <-rebooted:
				if !tc.wantReboot {
					t.Error("the node rebooted")
				}
			case <-time.After(3 * time.Second):
				if tc.wantReboot {
					t.Error("the node did not reboot")
				}
			}
		})
	}
}

// TestOneUpgradeAtATime refuses a second upgrade while one runs.
func TestOneUpgradeAtATime(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	started, release := make(chan struct{}), make(chan struct{})
	s.InstallImage = func(context.Context, upgrade.Header, io.Reader) (upgrade.Result, error) {
		close(started)
		<-release
		return upgrade.Result{}, nil
	}
	addr := serve(t, s, c, c.pool)
	conn := dial(t, addr, c.clients[pki.RoleOperator])
	send := func() error {
		stream := conn.Upgrade(context.Background())
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{Image: &nodev1.ImageHeader{Version: "0.2.0"}}}})
		_, err := stream.CloseAndReceive()
		return err
	}
	first := make(chan error, 1)
	go func() { first <- send() }()
	<-started
	if err := send(); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("a second upgrade: %v, want aborted", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Error(err)
	}
}

// lockWatcher runs commands as the fake runner, showing each sfdisk call to check first.
type lockWatcher struct {
	*fakeRunner
	check func(args []string)
}

func (w lockWatcher) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "sfdisk" {
		w.check(args)
	}
	return w.fakeRunner.Run(ctx, name, args...)
}

// TestUpgradeSharesTheStorageLock reads the boot disk's partition table only while holding the
// lock ApplyIdentity and ResetVolume change the node's storage under.
func TestUpgradeSharesTheStorageLock(t *testing.T) {
	s, r := newTestServer(t, normal, vda)
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=0.1.0\nCHALKOS_CLUSTER=lab\nCHALKOS_ROLE=worker\n")
	write(t, s.Paths.Cmdline, "usrhash="+strings.Repeat("ab", 32)+"\n")
	calls := 0
	s.Run = lockWatcher{r, func(args []string) {
		calls++
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Errorf("sfdisk %v ran without the storage lock", args)
		}
	}}
	sum := bytes.Repeat([]byte{1}, 32)
	h := upgrade.Header{ImageID: "chalkos", Version: "0.2.0", Cluster: "lab", Role: "worker", Architecture: "x86-64", RootHash: bytes.Repeat([]byte{2}, 32),
		StoreSize: 1, VeritySize: 1, UKISize: 1, StoreSHA256: sum, VeritySHA256: sum, UKISHA256: sum}
	// The fake sfdisk prints no table, which ends the upgrade.
	if _, err := s.installImage(context.Background(), h, bytes.NewReader(nil)); err == nil {
		t.Fatal("the upgrade went on without a partition table")
	}
	if calls == 0 {
		t.Error("the upgrade did not read the partition table")
	}
	if !s.mu.TryLock() {
		t.Fatal("the upgrade kept the storage lock")
	}
	s.mu.Unlock()
}
