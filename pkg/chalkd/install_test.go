package chalkd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/storage"
)

const installIdentity = `{"hostname": "n1", "storage": {"disks": {"system": {"ref": {"serial": "chalk-target"}, "seed": "s", "repart": {}}}, "volumes": {}, "fallback": "recovery-key", "encryption": "tpm2"}}`

func header(target any) *nodev1.InstallHeader {
	h := &nodev1.InstallHeader{
		Identity:        installIdentity,
		NodeCertificate: []byte("cert"),
		NodeKey:         []byte("key"),
		CaCertificate:   []byte("ca"),
		FallbackSecret:  "recovery",
	}
	switch t := target.(type) {
	case *nodev1.InstallHeader_InPlace:
		h.Target = t
	case *nodev1.InstallHeader_Disk:
		h.Target = t
	}
	return h
}

func sendInstall(t *testing.T, s *Server, h *nodev1.InstallHeader, image []byte) error {
	t.Helper()
	c := newCreds(t)
	conn := dial(t, serve(t, s, c, nil), nil)
	stream := conn.Install(context.Background())
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: h}}); err != nil {
		t.Fatal(err)
	}
	for len(image) > 0 {
		n := min(len(image), 1000)
		if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Chunk{Chunk: &nodev1.ImageChunk{Data: image[:n]}}}); err != nil {
			t.Fatal(err)
		}
		image = image[n:]
	}
	_, err := stream.CloseAndReceive()
	return err
}

func TestInstallInPlace(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	rebooted := make(chan struct{})
	s.RebootNode = func() { close(rebooted) }

	if err := sendInstall(t, s, header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil); err != nil {
		t.Fatal(err)
	}
	if string(got.Identity) != installIdentity || string(got.NodeKey) != "key" || string(got.CA) != "ca" || got.FallbackSecret != "recovery" ||
		got.Section.Disks[storage.SystemDisk].Ref.Selector.Serial != "chalk-target" {
		t.Errorf("install request = %+v", got)
	}
	select {
	case <-rebooted:
	case <-time.After(5 * time.Second):
		t.Fatal("the node did not reboot after installing")
	}
	err := sendInstall(t, s, header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second install: %v", err)
	}
}

func TestInstallFromMedia(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	image := bytes.Repeat([]byte("chalkos"), 5000)
	sum := sha256.Sum256(image)
	var got install.MediaRequest
	var streamed []byte
	s.FromMedia = func(_ context.Context, req install.MediaRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Image)
		return err
	}
	h := header(&nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.ImageSize = uint64(len(image))
	h.ImageSha256 = sum[:]
	h.SystemDefinitions = map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}
	h.WipeDisk = true

	if err := sendInstall(t, s, h, image); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamed, image) {
		t.Errorf("streamed %d bytes, want the %d of the image", len(streamed), len(image))
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || got.ImageSize != int64(len(image)) ||
		!bytes.Equal(got.ImageSHA256, sum[:]) || !got.WipeDisk || got.SystemDefinitions["50-state.conf"] == "" {
		t.Errorf("media request = %+v", got)
	}
}

func TestInstallTargetMustMatchImage(t *testing.T) {
	for _, c := range []struct {
		name      string
		installer bool
		target    any
	}{
		{"in place on the installer", true, &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}},
		{"onto a disk from a role image", false, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = c.installer
			err := sendInstall(t, s, header(c.target), nil)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestInstallFailureCanBeRepeated(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	attempts := 0
	s.InPlace = func(context.Context, install.Request) error {
		attempts++
		if attempts == 1 {
			return errors.New("enroll the fallback key on var: TPM unavailable")
		}
		return nil
	}
	var rebooted atomic.Bool
	s.RebootNode = func() { rebooted.Store(true) }
	in := header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	if err := sendInstall(t, s, in, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if rebooted.Load() {
		t.Fatal("rebooted after a failed install")
	}
	if err := sendInstall(t, s, in, nil); err != nil {
		t.Fatalf("the install could not be repeated: %v", err)
	}
}
