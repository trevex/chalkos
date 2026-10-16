package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// baseConfig is a node of the test server's image, a worker of the cluster lab without
// Kubernetes, with a Demo whose token is a secret.
const baseConfig = `
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {}
---
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: metal}
spec: {source: ./images/metal}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {imageRef: metal}
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec: {disk: {path: /dev/vda}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

const timeSyncConfig = `---
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: time}
spec: {servers: [{host: ntp.lan}]}
`

// schemaFixtures hold the schemas of the built-in kinds, of a networkd Network and of the Demo
// extension kind, whose token is a secretRef.
var schemaFixtures = []string{
	"../../test/fixtures/kinds/builtin",
	"../../test/fixtures/config/networkd/schemas",
	"../../test/fixtures/kinds/image/schemas",
}

// fixtureSchemas returns the fixture schemas by file name, as an install header carries them.
func fixtureSchemas(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, dir := range schemaFixtures {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = b
		}
	}
	return files
}

func writeSchemas(t *testing.T, dir string) {
	t.Helper()
	for name, b := range fixtureSchemas(t) {
		write(t, filepath.Join(dir, name), string(b))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hashOf(t *testing.T, text string) string {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	h, err := config.Hash(docs)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// configServer is a normal-mode server that runs baseConfig: STATE holds the set, the identity
// it translates to and that identity's storage section, pinned. The image has the consumers
// demo, of the Demo kind, and timesync, of the TimeSync kind.
func configServer(t *testing.T) (*Server, *fakeRunner) {
	t.Helper()
	s, r := newTestServer(t, normal, vda, vdb)
	writeSchemas(t, s.Identity.Schemas)
	write(t, s.Identity.Consumers, `{
	  "demo": {"kinds": ["Demo"], "restartOnChange": true},
	  "timesync": {"kinds": ["TimeSync"], "restartOnChange": true}
	}`)
	schemas, err := schema.LoadSchemas(s.Identity.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	c, err := configure(baseConfig, schemas, config.Target{Role: "worker", Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	var id manifest.Identity
	if err := json.Unmarshal(c.identity, &id); err != nil {
		t.Fatal(err)
	}
	section, err := json.Marshal(id.Storage)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "storage.json"), string(section))
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`"}}}}`)
	write(t, filepath.Join(s.Paths.StateDir, "identity.json"), string(c.identity))
	if err := store.Write(s.Identity.Config, c.stored); err != nil {
		t.Fatal(err)
	}
	return s, r
}

func sendConfig(s *Server, text string) (*nodev1.ApplyConfigResponse, error) {
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: text}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func TestApplyConfig(t *testing.T) {
	s, r := configServer(t)
	next := strings.NewReplacer("token: s3cret", "token: n3w", "name: first", "name: second").Replace(baseConfig)

	resp, err := sendConfig(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"networkctl reload", "systemctl show --property=ActiveState --value demo", "systemctl try-restart demo"}; !slices.Equal(r.calls, want) {
		t.Errorf("commands = %v, want %v", r.calls, want)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"demo"}) || len(resp.Changes) != 0 {
		t.Errorf("response = %+v", resp)
	}
	if v, err := store.Version(s.Identity.Config); err != nil || v != hashOf(t, next) {
		t.Errorf("stored version = %q, %v; want the hash of the delivered documents", v, err)
	}
	stored := readFile(t, filepath.Join(s.Identity.Config, "demo.example.org", "Demo", "one.json"))
	if !strings.Contains(stored, `"name":"second"`) || !strings.Contains(stored, `"replicas":1`) {
		t.Errorf("stored Demo = %s, want the new name and the schema's default replicas", stored)
	}
	if info, err := os.Stat(filepath.Join(s.Identity.Config, "secrets", "demo.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("stored Secret: %v, %v", info, err)
	}
	var id manifest.Identity
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(s.Paths.StateDir, "identity.json"))), &id); err != nil ||
		id.Hostname != "n1" || id.Cluster != "lab" || id.Role != "worker" || id.Platform != "metal" {
		t.Errorf("identity on STATE = %+v, %v", id, err)
	}
	if got := readFile(t, filepath.Join(s.Identity.RunDir, "credentials", "demo", "demo.token")); got != "n3w" {
		t.Errorf("demo's credential = %q", got)
	}
	runConfig, err := filepath.EvalSymlinks(filepath.Join(s.Identity.RunDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	err = filepath.WalkDir(runConfig, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		files = append(files, strings.TrimPrefix(path, runConfig+"/"))
		if text := readFile(t, path); strings.Contains(text, "n3w") || strings.Contains(text, "s3cret") {
			t.Errorf("%s holds a secret value: %s", path, text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, "demo.example.org/Demo/one.json") || slices.ContainsFunc(files, func(f string) bool { return strings.Contains(f, "Secret") }) {
		t.Errorf("/run/chalkos/config holds %v", files)
	}
}

// TestApplyConfigRestartsByKind restarts the consumers of the kinds whose documents changed, or a
// Secret one of them references.
func TestApplyConfigRestartsByKind(t *testing.T) {
	for name, c := range map[string]struct {
		next string
		want []string
	}{
		"unchanged":                  {baseConfig, nil},
		"a Secret a Demo references": {strings.Replace(baseConfig, "token: s3cret", "token: n3w", 1), []string{"demo"}},
		"a Demo":                     {strings.Replace(baseConfig, "name: first", "name: second", 1), []string{"demo"}},
		"a TimeSync added":           {baseConfig + timeSyncConfig, []string{"timesync"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := configServer(t)
			resp, err := sendConfig(s, c.next)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(resp.RestartedUnits, c.want) {
				t.Errorf("restarted %v, want %v", resp.RestartedUnits, c.want)
			}
		})
	}
}

func TestApplyConfigRefusesAnInvalidSet(t *testing.T) {
	s, r := configServer(t)
	_, err := sendConfig(s, strings.Replace(baseConfig, "spec: {imageRef: metal}", "spec: {}", 1))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), `Node "n1"`) {
		t.Fatalf("err = %v, want a refusal naming the Node", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v for a refused config", r.calls)
	}
	if v, _ := store.Version(s.Identity.Config); v != hashOf(t, baseConfig) {
		t.Error("stored a refused config")
	}
}

// TestApplyConfigOnAKubernetesImage translates config with the running image's image.json.
func TestApplyConfigOnAKubernetesImage(t *testing.T) {
	s, _ := configServer(t)
	kubernetesImage(t, s)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	var id manifest.Identity
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(s.Paths.StateDir, "identity.json"))), &id); err != nil {
		t.Fatal(err)
	}
	if k := id.Kubernetes; k == nil || k.NodeName != "n1" || !slices.Equal(k.NodeIPs, []string{"192.168.100.21"}) {
		t.Errorf("Kubernetes identity = %+v", id.Kubernetes)
	}
}

func TestApplyConfigRefusesMalformedImageFacts(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Identity.KubernetesImage, `{"version": "1.37.1"`)
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "the image's Kubernetes facts") {
		t.Fatalf("err = %v, want a failure naming the image's Kubernetes facts", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

// TestApplyConfigStartsFailedConsumers: a consumer that failed, such as one that found no
// document of its kind at boot, is started again with the new config, which try-restart would not
// do. One that is active or inactive is try-restarted, so a stopped unit stays stopped.
func TestApplyConfigStartsFailedConsumers(t *testing.T) {
	for state, want := range map[string][]string{
		"failed":   {"systemctl reset-failed demo", "systemctl restart demo"},
		"active":   {"systemctl try-restart demo"},
		"inactive": {"systemctl try-restart demo"},
	} {
		t.Run(state, func(t *testing.T) {
			s, r := configServer(t)
			r.rules = append(r.rules, rule{prefix: "systemctl show --property=ActiveState --value demo", out: state + "\n"})
			if _, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1)); err != nil {
				t.Fatal(err)
			}
			var restarts []string
			for _, c := range r.calls {
				if strings.HasPrefix(c, "systemctl ") && !strings.HasPrefix(c, "systemctl show ") {
					restarts = append(restarts, c)
				}
			}
			if !slices.Equal(restarts, want) {
				t.Errorf("commands = %v, want %v", restarts, want)
			}
		})
	}
}

// TestApplyConfigFailingRestartNamesTheUnits: once the new set is stored, a retry finds nothing
// changed, so the error names the units still to restart.
func TestApplyConfigFailingRestartNamesTheUnits(t *testing.T) {
	s, r := configServer(t)
	r.rules = append(r.rules, rule{prefix: "systemctl try-restart demo", err: errors.New("exit status 1")})
	next := strings.Replace(baseConfig, "name: first", "name: second", 1) + timeSyncConfig
	_, err := sendConfig(s, next)
	if err == nil || !strings.Contains(err.Error(), "restart demo by hand") || strings.Contains(err.Error(), "timesync by hand") {
		t.Fatalf("err = %v, want it to name demo alone", err)
	}
	if !slices.Contains(r.calls, "systemctl try-restart timesync") {
		t.Errorf("commands = %v; a failed restart must not stop the others", r.calls)
	}
}

// TestApplyConfigFailureKeepsKubernetesRestarts: the identity is on STATE once applyIdentity
// ran, so a retry would find it unchanged; Kubernetes is restarted even when the config fails.
func TestApplyConfigFailureKeepsKubernetesRestarts(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	// STATE holds a file where the config link belongs, which the store refuses to read.
	write(t, s.Identity.Config, "not a config set")
	if _, err := apply(s, kubernetesIdentity("n1"), ""); err == nil || !strings.Contains(err.Error(), "stored config") {
		t.Fatalf("err = %v, want the config's failure", err)
	}
	if !slices.Contains(r.calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("commands = %v, want Kubernetes restarted", r.calls)
	}
}

func TestStatusReportsConfigVersion(t *testing.T) {
	s, _ := configServer(t)
	if got := status(t, s).ConfigVersion; got != hashOf(t, baseConfig) {
		t.Errorf("config version = %q, want the hash of the delivered documents", got)
	}
}

// TestResetVolumeTakesConfig resets a volume as the delivered config defines it.
func TestResetVolumeTakesConfig(t *testing.T) {
	s, _ := configServer(t)
	reset := func(text string) error {
		_, err := s.ResetVolume(context.Background(), connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: "data", Config: text, FallbackSecret: "x"}))
		return err
	}
	if err := reset(baseConfig); connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "the node has no volume data") {
		t.Errorf("a volume the node lacks: %v", err)
	}
	if err := reset("kind: Nope\n"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid config: %v", err)
	}
}

// extraConfig is baseConfig with the volume extra on a disk of its own, in the format given.
func extraConfig(format string) string {
	return baseConfig + `---
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: extra}
spec: {disk: {serial: chalk-extra}, format: ` + format + `, mountPoint: /srv/extra}
`
}

// TestResetVolumeThroughConfig resets a volume through the RPC, as the delivered config
// defines it.
func TestResetVolumeThroughConfig(t *testing.T) {
	s, r := configServer(t)
	schemas, err := schema.LoadSchemas(s.Identity.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	c, err := configure(extraConfig("ext4"), schemas, config.Target{Role: "worker", Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	var id manifest.Identity
	if err := json.Unmarshal(c.identity, &id); err != nil {
		t.Fatal(err)
	}
	section, err := json.Marshal(id.Storage)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "storage.json"), string(section))
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {
	  "system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`"}},
	  "extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": {"extra": "`+extraUUID+`"}}}}`)
	write(t, filepath.Join(s.Paths.StateDir, "identity.json"), string(c.identity))
	partitionLink(t, s, "vdb", "vdb1", "253:17", "1")
	r.rules = []rule{
		{prefix: "blkid -p -o export /dev/vdb", err: &node.ToolError{Command: "blkid", Code: 2}},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "extra"), out: `[{"file":"` + filepath.Join(dir, "disks", "extra", "10-extra.conf") + `","uuid":"` + newUUID + `"}]`},
		{prefix: "systemd-repart --dry-run=no --json=short --definitions=" + filepath.Join(dir, "disks", "system"), out: `[{"file":"` + filepath.Join(dir, "disks", "system", "50-var.conf") + `","uuid":"` + varUUID + `"}]`},
		{prefix: "blkid -p -o value -s TYPE", out: "crypto_LUKS\n"},
		{prefix: "cryptsetup luksDump --dump-json-metadata", out: luksDump},
	}

	if _, err := s.ResetVolume(context.Background(), connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: "extra", Config: extraConfig("xfs"), FallbackSecret: secret})); err != nil {
		t.Fatal(err)
	}
	if !contains(r.calls, "wipefs --all /dev/vdb1") || !contains(r.calls, "systemctl start systemd-cryptsetup@extra.service srv-extra.mount") {
		t.Errorf("the volume was not wiped and created again: %v", r.calls)
	}
	recorded, err := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if err != nil || recorded.Volumes["extra"].Format != "xfs" {
		t.Errorf("recorded volume extra = %+v, %v; want it in the config's xfs", recorded.Volumes["extra"], err)
	}
}
