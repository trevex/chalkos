package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

const installIdentity = `{"hostname": "n1", "cluster": "lab", "role": "worker", "platform": "metal", "storage": {"disks": {"system": {"ref": {"serial": "chalk-target"}, "seed": "s", "repart": {}}}, "volumes": {}, "fallback": "recovery-key", "encryption": "tpm2"}}`

func header(target any) *nodev1.InstallHeader {
	h := &nodev1.InstallHeader{
		Identity:        installIdentity,
		NodeCertificate: []byte("cert"),
		NodeKey:         []byte("key"),
		CaCertificate:   []byte("ca"),
		FallbackSecret:  "recovery",
		KubernetesShare: []byte("share"),
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
	if string(got.Identity) != installIdentity || string(got.NodeKey) != "key" || string(got.CA) != "ca" || got.FallbackSecret != "recovery" || string(got.KubernetesShare) != "share" ||
		got.Section.Disks[storage.SystemDisk].Ref.Selector.Serial != "chalk-target" {
		t.Errorf("install request = %+v", got)
	}
	select {
	case <-rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot after installing")
	}
	err := sendInstall(t, s, header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}), nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second install: %v", err)
	}
}

func TestInstallFromParts(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	parts := bytes.Repeat([]byte("chalkos"), 5000)
	var got install.PartsRequest
	var streamed []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Parts)
		return err
	}
	h := header(&nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Image = &nodev1.ImageHeader{
		Version: "0.1.0", ImageId: "chalkos", Cluster: "lab", Role: "worker", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1},
		Store: &nodev1.ImagePart{Size: 1, Sha256: []byte{2}}, HashTree: &nodev1.ImagePart{Size: 3, Sha256: []byte{4}},
		Uki: &nodev1.ImagePart{Size: 5, Sha256: []byte{6}}, BootLoader: &nodev1.ImagePart{Size: 7, Sha256: []byte{8}},
	}
	h.SystemDefinitions = map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}
	h.WipeDisk = true

	if err := sendInstall(t, s, h, parts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamed, parts) {
		t.Errorf("streamed %d bytes, want the %d of the image's parts", len(streamed), len(parts))
	}
	want := upgrade.Header{
		ImageID: "chalkos", Version: "0.1.0", Cluster: "lab", Role: "worker", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1},
		StoreSize: 1, StoreSHA256: []byte{2}, VeritySize: 3, VeritySHA256: []byte{4},
		UKISize: 5, UKISHA256: []byte{6}, BootLoaderSize: 7, BootLoaderSHA256: []byte{8},
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || !reflect.DeepEqual(got.Image, want) ||
		!got.WipeDisk || got.SystemDefinitions["50-state.conf"] == "" {
		t.Errorf("parts request = %+v", got)
	}
}

// TestInstallImageMatchesTheTarget refuses an installer install without an image, and an install
// in place with one.
func TestInstallImageMatchesTheTarget(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.InPlace = func(context.Context, install.Request) error {
		t.Error("installed in place")
		return nil
	}
	in := header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	in.Image = &nodev1.ImageHeader{Version: "0.1.0"}
	if err := sendInstall(t, s, in, nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("in place with an image: %v", err)
	}
	s.Installer = true
	s.FromParts = func(context.Context, install.PartsRequest) error {
		t.Error("installed onto the disk")
		return nil
	}
	if err := sendInstall(t, s, header(&nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}), nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("onto a disk without an image: %v", err)
	}
}

// TestInstallImageOfTheIdentity refuses an image of another cluster, role or platform than the
// identity's.
func TestInstallImageOfTheIdentity(t *testing.T) {
	for _, tc := range []struct {
		cluster, role, platform, want string
	}{
		{"prod", "worker", "metal", `the image is of the cluster "prod" and the role "worker", but the identity names "lab" and "worker"`},
		{"lab", "controlplane", "metal", `the image is of the cluster "lab" and the role "controlplane", but the identity names "lab" and "worker"`},
		{"lab", "worker", "kvm", `the image is built for the platform "kvm", but the identity names "metal"`},
	} {
		s, _ := newTestServer(t, maintenance, vda)
		s.Installer = true
		s.FromParts = func(context.Context, install.PartsRequest) error {
			t.Error("installed onto the disk")
			return nil
		}
		h := header(&nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}})
		h.Image = &nodev1.ImageHeader{Version: "0.1.0", ImageId: "chalkos", Cluster: tc.cluster, Role: tc.role, Platform: tc.platform}
		if err := sendInstall(t, s, h, nil); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("an image of %s, %s and %s: %v, want %q", tc.cluster, tc.role, tc.platform, err, tc.want)
		}
	}
}

// TestInstallInPlaceOfTheImage refuses to install in place an identity of another cluster, role or
// platform than the running image's.
func TestInstallInPlaceOfTheImage(t *testing.T) {
	for _, tc := range []struct{ cluster, role, platform, want string }{
		{"prod", "worker", "metal", `the identity is of the cluster "prod" and the role "worker", but the node's image is of "lab" and "worker"`},
		{"lab", "controlplane", "metal", `the identity is of the cluster "lab" and the role "controlplane", but the node's image is of "lab" and "worker"`},
		{"lab", "worker", "kvm", `the identity names the platform "kvm", but the node's image is built for "metal"; changing a node's platform is a reinstall`},
	} {
		s, _ := newTestServer(t, maintenance, vda)
		s.InPlace = func(context.Context, install.Request) error {
			t.Error("installed in place")
			return nil
		}
		h := header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
		h.Identity = strings.NewReplacer(`"cluster": "lab"`, `"cluster": "`+tc.cluster+`"`, `"role": "worker"`, `"role": "`+tc.role+`"`, `"platform": "metal"`, `"platform": "`+tc.platform+`"`).Replace(installIdentity)
		if err := sendInstall(t, s, h, nil); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("an identity of %s, %s and %s: %v, want %q", tc.cluster, tc.role, tc.platform, err, tc.want)
		}
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

// workerCluster is a worker image's kubernetes.json.
const workerCluster = `{"kind": "worker", "endpoint": "https://192.168.100.11:6443", "version": "1.37.1",
  "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local",
  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`

// workerConfig is baseConfig for a Kubernetes worker with a fixed address.
var workerConfig = strings.Replace(baseConfig, "spec: {imageRef: metal}", "spec: {type: worker, imageRef: metal}", 1) + `---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Kubelet
metadata: {name: kubelet}
spec: {nodeIPs: [192.168.100.21]}
`

func TestInstallInPlaceWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	writeSchemas(t, s.Identity.Schemas)
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	h := header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
	h.Identity, h.Config = "", baseConfig

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	var id manifest.Identity
	if err := json.Unmarshal(got.Identity, &id); err != nil || id.Hostname != "n1" || id.Role != "worker" || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", got.Identity, err)
	}
	if got.Section.Disks[storage.SystemDisk].Ref.Path != "/dev/vda" {
		t.Errorf("section = %+v", got.Section)
	}
	if got.Config == nil || got.Config.Version != hashOf(t, baseConfig) || len(got.Config.Documents) != 6 {
		t.Errorf("config = %+v", got.Config)
	}
}

func TestInstallFromPartsWithConfig(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	var got install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		_, err := io.ReadAll(req.Parts)
		return err
	}
	h := header(&nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Identity, h.Config = "", workerConfig
	h.Schemas = fixtureSchemas(t)
	h.KubernetesCluster = []byte(workerCluster)
	h.Image = &nodev1.ImageHeader{Version: "0.1.0", ImageId: "chalkos", Cluster: "lab", Role: "worker", Platform: "metal", Architecture: "x86-64"}

	if err := sendInstall(t, s, h, nil); err != nil {
		t.Fatal(err)
	}
	var id manifest.Identity
	if err := json.Unmarshal(got.Identity, &id); err != nil || id.Role != "worker" || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", got.Identity, err)
	}
	if k := got.Kubernetes; k == nil || k.NodeName != "n1" || !slices.Equal(k.NodeIPs, []string{"192.168.100.21"}) {
		t.Errorf("Kubernetes identity = %+v", got.Kubernetes)
	}
	if got.Config == nil || got.Config.Version != hashOf(t, workerConfig) {
		t.Errorf("config = %+v", got.Config)
	}
}

func TestInstallRefusesConfig(t *testing.T) {
	schemas := fixtureSchemas(t)
	fromInstaller := func(h *nodev1.InstallHeader) {
		h.Identity, h.Config, h.Schemas = "", workerConfig, schemas
		h.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}
		h.Image = &nodev1.ImageHeader{Version: "0.1.0", Cluster: "lab", Role: "worker", Platform: "metal"}
	}
	for name, c := range map[string]struct {
		installer bool
		edit      func(*nodev1.InstallHeader)
		want      string
	}{
		"with an identity": {false, func(h *nodev1.InstallHeader) { h.Config = baseConfig }, "both config and an identity"},
		"invalid": {false, func(h *nodev1.InstallHeader) {
			h.Identity, h.Config = "", strings.Replace(baseConfig, "spec: {imageRef: metal}", "spec: {}", 1)
		}, `Node "n1"`},
		"in place with schemas": {false, func(h *nodev1.InstallHeader) {
			h.Identity, h.Config, h.Schemas = "", baseConfig, map[string][]byte{"x.json": []byte("{}")}
		}, "running image"},
		"from the installer without schemas": {true, func(h *nodev1.InstallHeader) {
			h.Identity, h.Config = "", baseConfig
			h.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Path: "/dev/vdb"}}
			h.Image = &nodev1.ImageHeader{Version: "0.1.0", Cluster: "lab", Role: "worker", Platform: "metal"}
		}, "schemas"},
		"neither an identity nor config": {false, func(h *nodev1.InstallHeader) { h.Identity = "" }, "the header delivers no identity or config"},
		"from the installer with an invalid kubernetes.json": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			h.KubernetesCluster = []byte(`{"kind": "worker"`)
		}, "kubernetes.json"},
		"from the installer with a schema outside schemas/": {true, func(h *nodev1.InstallHeader) {
			fromInstaller(h)
			h.KubernetesCluster = []byte(workerCluster)
			h.Schemas = map[string][]byte{"../x.json": []byte("{}")}
		}, `"../x.json" is not the name of a schema file`},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t, maintenance, vda)
			s.Installer = c.installer
			writeSchemas(t, s.Identity.Schemas)
			s.InPlace = func(context.Context, install.Request) error { t.Error("installed in place"); return nil }
			s.FromParts = func(context.Context, install.PartsRequest) error { t.Error("installed onto the disk"); return nil }
			h := header(&nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}})
			c.edit(h)
			if err := sendInstall(t, s, h, nil); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}
