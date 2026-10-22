package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// baseConfig is a node of the test server's image without Kubernetes, with a Demo whose token is a
// secret.
const baseConfig = `
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

// configServer is a normal-mode server of node n1, as its node certificate names it, that runs
// baseConfig: STATE holds the set, the identity it translates to and that identity's storage
// section, pinned. The image has the built-in effects, and demo.service, granted the Demo kind,
// which restarts when the output demo, the Demo documents, changes.
func configServer(t *testing.T) (*Server, *fakeRunner) {
	t.Helper()
	s, r := newTestServer(t, normal, vda, vdb)
	withNodeCertificate(t, s)
	writeSchemas(t, s.Loader.Schemas)
	writeServices(t, s, `{"demo": {"kinds": ["Demo"]}}`)
	writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `cat > "$OUT/demo.json"`, "")})
	writeEffects(t, s, `{"demo": {"on": ["demo"], "restart": ["demo.service"]}}`)
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	c, err := configure("n1", baseConfig, schemas, config.Target{Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(c.Identity, &id); err != nil {
		t.Fatal(err)
	}
	section, err := json.Marshal(id.Storage)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Paths.StateDir, "storage")
	write(t, filepath.Join(dir, "storage.json"), string(section))
	write(t, filepath.Join(dir, "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "`+varUUID+`"}}}}`)
	if err := store.Write(slotOf(t, s), c); err != nil {
		t.Fatal(err)
	}
	// As at boot: the networkd units, the time sources the identity gives and the outputs are in
	// place.
	if err := s.Loader.Load(); err != nil {
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
	if want := []string{"systemctl show --property=ActiveState --value demo.service", "systemctl try-restart demo.service"}; !slices.Equal(r.calls, want) {
		t.Errorf("commands = %v, want %v", r.calls, want)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"demo.service"}) || len(resp.Changes) != 0 {
		t.Errorf("response = %+v", resp)
	}
	if v, err := store.Version(slotOf(t, s)); err != nil || v != hashOf(t, next) {
		t.Errorf("stored version = %q, %v; want the hash of the delivered documents", v, err)
	}
	stored := readFile(t, filepath.Join(slotOf(t, s), "documents", "demo.example.org", "Demo", "one.json"))
	if !strings.Contains(stored, `"name":"second"`) || !strings.Contains(stored, `"replicas":1`) {
		t.Errorf("stored Demo = %s, want the new name and the schema's default replicas", stored)
	}
	if info, err := os.Stat(filepath.Join(slotOf(t, s), "documents", "secrets", "demo.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("stored Secret: %v, %v", info, err)
	}
	var id identity.Identity
	if err := json.Unmarshal([]byte(identityOf(t, s)), &id); err != nil ||
		id.Hostname != "n1" || id.Platform != "metal" {
		t.Errorf("identity on STATE = %+v, %v", id, err)
	}
	if got := readFile(t, filepath.Join(s.Loader.RunDir, "credentials", "demo", "demo.token")); got != "n3w" {
		t.Errorf("demo's credential = %q", got)
	}
	if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); !strings.Contains(got, `"name":"second"`) || !strings.Contains(got, `"node":{"name":"n1"}`) || strings.Contains(got, "n3w") {
		t.Errorf("the output demo = %s, want the new Demo for n1 without the secret", got)
	}
	runConfig, err := filepath.EvalSymlinks(filepath.Join(s.Loader.RunDir, "config"))
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

// TestApplyConfigTakesTheNodesNameFromItsCertificate translates the config, whose documents name
// no node, for the node its node certificate names, names that node in a refusal, and translates
// no config on a node without a node certificate.
func TestApplyConfigTakesTheNodesNameFromItsCertificate(t *testing.T) {
	s, _ := configServer(t)
	doc := baseConfig
	if _, err := sendConfig(s, doc); err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal([]byte(identityOf(t, s)), &id); err != nil {
		t.Fatal(err)
	}
	if id.Hostname != "n1" {
		t.Errorf("hostname = %q, want n1, the node certificate's name", id.Hostname)
	}
	_, err := sendConfig(s, strings.Replace(doc, "{disk: {path: /dev/vda}}", "{disk: {path: /dev/vda}, var: {size: 1G}}", 1))
	if err == nil || !strings.Contains(err.Error(), "the config takes a reinstall of n1: ") || !strings.Contains(err.Error(), "only a reinstall of n1 carries it out") {
		t.Errorf("err = %v, want a refusal naming n1", err)
	}
	s.Certificate = nil
	if _, err := sendConfig(s, doc); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "node certificate") {
		t.Errorf("err = %v, want an internal error for the missing node certificate", err)
	}
}

func TestApplyConfigRefusesAnInvalidSet(t *testing.T) {
	s, r := configServer(t)
	_, err := sendConfig(s, strings.Replace(baseConfig, "{disk: {path: /dev/vda}}", "{disk: {path: vda}}", 1))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), `SystemDisk "system"`) {
		t.Fatalf("err = %v, want a refusal naming the SystemDisk", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v for a refused config", r.calls)
	}
	if v, _ := store.Version(slotOf(t, s)); v != hashOf(t, baseConfig) {
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
	var id identity.Identity
	if err := json.Unmarshal([]byte(identityOf(t, s)), &id); err != nil {
		t.Fatal(err)
	}
	if k := id.Kubernetes; k == nil || k.NodeName != "n1" || !slices.Equal(k.NodeIPs, []string{"192.168.100.21"}) {
		t.Errorf("Kubernetes identity = %+v", id.Kubernetes)
	}
}

func TestApplyConfigRefusesMalformedImageFacts(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Loader.KubernetesImage, `{"version": "1.37.1"`)
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "the image's Kubernetes facts") {
		t.Fatalf("err = %v, want a failure naming the image's Kubernetes facts", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

// TestApplyConfigFailingRestartNamesTheUnits: the error names the units still to restart, for an
// operator who does not deliver the config again: every unit of the systemctl call that failed,
// which does not tell which of them failed. A failed call does not stop the others.
func TestApplyConfigFailingRestartNamesTheUnits(t *testing.T) {
	s, r := configServer(t)
	r.rules = append(r.rules,
		rule{prefix: "systemctl show --property=ActiveState --value chalkos-chrony-dhcp.service", out: "failed\n"},
		rule{prefix: "systemctl try-restart demo.service", err: errors.New("exit status 1")})
	next := strings.Replace(baseConfig, "name: first", "name: second", 1) + timeSyncConfig
	_, err := sendConfig(s, next)
	if err == nil || !strings.Contains(err.Error(), "try-restart demo.service: exit status 1; the delivery is applied, so restart demo.service by hand") || strings.Contains(err.Error(), "chalkos-chrony-dhcp.service by hand") {
		t.Fatalf("err = %v, want it to name demo.service alone", err)
	}
	if !slices.Contains(r.calls, "systemctl restart chalkos-chrony-dhcp.service") {
		t.Errorf("commands = %v; a failed restart must not stop the others", r.calls)
	}
}

// TestApplyConfigReloadsThenRestarts: once the delivery is applied, chalkd reloads the units of
// the due effects that run, in one call, then runs their commands, before it restarts any unit; a
// unit it restarts it does not reload. It restarts the units that start too with restart, a
// failed one after reset-failed, and the rest with try-restart, each kind in one call, and names
// the restarted units. An effect fires on a changed output or on its rule.
func TestApplyConfigReloadsThenRestarts(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Loader.Effects, `{
	  "demo": {"on": ["demo"], "restart": ["demo.service"]},
	  "demo-reload": {"on": ["demo"], "reload": ["demo.service"], "run": "democtl reload"},
	  "logger": {"on": ["demo"], "reload": ["logger.service"]},
	  "stopped": {"when": "true", "reload": ["stopped.service"]},
	  "watcher": {"when": "config[\"demo.example.org/v1alpha1\"].Demo.one.spec.name != oldConfig[\"demo.example.org/v1alpha1\"].Demo.one.spec.name", "restart": ["watcher.service"]},
	  "starter": {"on": ["demo"], "restart": ["starter.service"], "start": true},
	  "broken": {"on": ["demo"], "restart": ["broken.service"]},
	  "idle": {"when": "false", "restart": ["idle.service"]}
	}`)
	r.rules = append(r.rules,
		rule{prefix: "systemctl show --property=ActiveState --value stopped.service", out: "inactive\n"},
		rule{prefix: "systemctl show --property=ActiveState --value broken.service", out: "failed\n"})
	resp, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	if err != nil {
		t.Fatal(err)
	}
	got := r.calls
	want := []string{
		"systemctl show --property=ActiveState --value demo.service",
		"systemctl show --property=ActiveState --value logger.service",
		"systemctl show --property=ActiveState --value stopped.service",
		"systemctl show --property=ActiveState --value broken.service",
		"systemctl show --property=ActiveState --value starter.service",
		"systemctl show --property=ActiveState --value watcher.service",
		"systemctl reload logger.service",
		"democtl reload",
		"systemctl reset-failed broken.service",
		"systemctl restart broken.service starter.service",
		"systemctl try-restart demo.service watcher.service",
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
	if !slices.Equal(resp.RestartedUnits, []string{"broken.service", "demo.service", "starter.service", "watcher.service"}) {
		t.Errorf("restarted %v", resp.RestartedUnits)
	}
}

// networkConfig is a networkd Network document, which the config loader renders as a unit.
const networkConfig = `---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec: {match: {name: enp1s0}, network: {address: [10.0.0.11/24]}}
`

// TestApplyConfigReloadsNetworkd: networkd reloads the units the config loader renders when the
// networkd documents change, and not for another change.
func TestApplyConfigReloadsNetworkd(t *testing.T) {
	s, r := configServer(t)
	for _, step := range []struct {
		name, config string
		reloads      bool
	}{
		{"unchanged", baseConfig, false},
		{"a Network added", baseConfig + networkConfig, true},
		{"the same Network", baseConfig + networkConfig, false},
		{"another Demo", strings.Replace(baseConfig, "name: first", "name: second", 1) + networkConfig, false},
		{"another address", baseConfig + strings.Replace(networkConfig, "10.0.0.11", "10.0.0.12", 1), true},
	} {
		r.calls = nil
		if _, err := sendConfig(s, step.config); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(r.calls, "systemctl reload systemd-networkd.service"); got != step.reloads {
			t.Errorf("%s: reloaded %v, want %v: %v", step.name, got, step.reloads, r.calls)
		}
	}
}

// TestApplyConfigReloadsTimeServers: chrony reloads its sources when the TimeSync's servers
// change, a server's host, port or NTS among them, and not for another change: whether the
// servers DHCP announces count is chalkos-chrony-dhcp's, which restarts.
func TestApplyConfigReloadsTimeServers(t *testing.T) {
	s, r := configServer(t)
	for _, step := range []struct {
		name, config string
		want         []string
	}{
		{"a TimeSync added", baseConfig + timeSyncConfig, []string{"chalkos-chrony-reload-sources", "systemctl try-restart chalkos-chrony-dhcp.service"}},
		{"the same", baseConfig + timeSyncConfig, nil},
		{"DHCP's servers", baseConfig + strings.Replace(timeSyncConfig, "spec: {servers", "spec: {dhcp: true, servers", 1), []string{"systemctl try-restart chalkos-chrony-dhcp.service"}},
		{"another server", baseConfig + strings.Replace(timeSyncConfig, "ntp.lan", "ntp2.lan", 1), []string{"chalkos-chrony-reload-sources", "systemctl try-restart chalkos-chrony-dhcp.service"}},
		{"another port", baseConfig + strings.Replace(timeSyncConfig, "{host: ntp.lan}", "{host: ntp2.lan, port: 4460}", 1), []string{"chalkos-chrony-reload-sources", "systemctl try-restart chalkos-chrony-dhcp.service"}},
		{"NTS off", baseConfig + strings.Replace(timeSyncConfig, "{host: ntp.lan}", "{host: ntp2.lan, port: 4460, nts: false}", 1), []string{"chalkos-chrony-reload-sources", "systemctl try-restart chalkos-chrony-dhcp.service"}},
		{"the same servers with another Demo", strings.Replace(baseConfig, "name: first", "name: second", 1) + strings.Replace(timeSyncConfig, "{host: ntp.lan}", "{host: ntp2.lan, port: 4460, nts: false}", 1), []string{"systemctl try-restart demo.service"}},
	} {
		r.calls = nil
		if _, err := sendConfig(s, step.config); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range r.calls {
			if !strings.HasPrefix(c, "systemctl show ") {
				got = append(got, c)
			}
		}
		if !slices.Equal(got, step.want) {
			t.Errorf("%s: commands %v, want %v", step.name, got, step.want)
		}
	}
}

// TestApplyConfigActsOnItselfInProcess: chalkd takes the effects on its own loops itself, and
// neither asks systemd nor names itself as restarted.
func TestApplyConfigActsOnItselfInProcess(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Loader.Effects, `{"cluster": {"when": "true", "chalkd": "restart"}, "share": {"when": "true", "chalkd": "reload"}}`)
	var acted [2]bool
	s.actedOnSelf = func(reload, restart bool) { acted = [2]bool{reload, restart} }
	resp, err := sendConfig(s, baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 || len(resp.RestartedUnits) != 0 {
		t.Errorf("ran %v, restarted %v", r.calls, resp.RestartedUnits)
	}
	if acted != [2]bool{false, true} {
		t.Errorf("acted on itself %v, want a restart, which a reload is part of", acted)
	}
}

// TestApplyConfigFailingReloadNamesTheUnit: a reload that fails is named for a reload by hand, and
// the restarts still run.
func TestApplyConfigFailingReloadNamesTheUnit(t *testing.T) {
	s, r := configServer(t)
	writeEffects(t, s, `{
	  "logger": {"on": ["demo"], "reload": ["logger.service"]},
	  "demo": {"on": ["demo"], "restart": ["demo.service"]}
	}`)
	r.rules = append(r.rules, rule{prefix: "systemctl reload logger.service", err: errors.New("exit status 1")})
	_, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	if err == nil || !strings.Contains(err.Error(), "reload logger.service: exit status 1; the delivery is applied, so reload logger.service by hand") {
		t.Fatalf("err = %v, want the reload named", err)
	}
	if !slices.Contains(r.calls, "systemctl try-restart demo.service") {
		t.Errorf("commands = %v; a failed reload must not stop the restarts", r.calls)
	}
}

// TestApplyConfigRestartsOnARuleThatFails: a rule that cannot be evaluated counts as true, so the
// unit restarts once more than it needed to rather than run on config it has not read. The
// response says why as a warning, without the values the rule read.
func TestApplyConfigRestartsOnARuleThatFails(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Loader.Effects, `{"demo": {"when": "config[\"demo.example.org/v1alpha1\"].Demo.one.spec.token.missing == \"s3cret\"", "restart": ["demo.service"]}}`)
	resp, err := sendConfig(s, baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"the rule of effect demo: a rule cannot be evaluated: no such key; taking the effect all the same"}
	if !slices.Equal(resp.Warnings, want) {
		t.Errorf("warnings = %q, want %q", resp.Warnings, want)
	}
	if !slices.Contains(r.calls, "systemctl try-restart demo.service") {
		t.Errorf("commands = %v", r.calls)
	}
}

// TestApplyConfigComparesFactsBeforeAndAfter: a rule reads a fact as it was before the delivery
// changed anything, as oldFacts, and as it is once the delivery is applied, as facts: here the
// host name of the node.json the delivery writes, which the test server has none of at first, as
// after a boot whose config loader failed.
func TestApplyConfigComparesFactsBeforeAndAfter(t *testing.T) {
	s, r := configServer(t)
	if err := os.Remove(filepath.Join(s.Loader.RunDir, "node.json")); err != nil {
		t.Fatal(err)
	}
	write(t, s.Paths.Facts, `{"node": {"file": "`+filepath.Join(s.Loader.RunDir, "node.json")+`", "fields": ["hostname"]}}`)
	write(t, s.Loader.Effects, `{"namer": {"when": "facts.node != oldFacts.node", "restart": ["namer.service"]}}`)
	for _, step := range []struct {
		name, config string
		restarts     bool
	}{
		{"the first node.json", baseConfig, true},
		{"the same", baseConfig, false},
		{"another host name", baseConfig + hostConfig("default", "h2"), true},
	} {
		r.calls = nil
		if _, err := sendConfig(s, step.config); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(r.calls, "systemctl try-restart namer.service"); got != step.restarts {
			t.Errorf("%s: restarted %v, want %v: %v", step.name, got, step.restarts, r.calls)
		}
	}
}

// TestApplyConfigFailureToStoreRestartsNothing: no unit restarts before the slot holds the new
// config, so one that cannot be written restarts nothing, and since the slot still holds the old
// config, a retry restarts what the change asks for rather than finding nothing changed.
func TestApplyConfigFailureToStoreRestartsNothing(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	slots := filepath.Dir(slotOf(t, s))
	// The directory of slots takes no new slot directory.
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	if _, err := sendConfig(s, workerConfig); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot's failure", err)
	}
	restart := "systemctl restart chalkos-kubernetes.service kubelet.service"
	if slices.Contains(r.calls, restart) {
		t.Errorf("commands = %v, want nothing restarted", r.calls)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.calls, restart) {
		t.Errorf("the retry ran %v, want Kubernetes restarted", r.calls)
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
	schemas, err := schema.LoadSchemas(s.Loader.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	c, err := configure("n1", extraConfig("ext4"), schemas, config.Target{Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(c.Identity, &id); err != nil {
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
	writeIdentity(t, s, string(c.Identity))
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

// TestGetConfig returns the running slot's documents as delivered, with the Secrets' values
// replaced by their hashes, and its version; staged, the staged slot's, which a node without a
// staged image does not have.
func TestGetConfig(t *testing.T) {
	s, _ := configServer(t)
	resp, err := s.GetConfig(context.Background(), connect.NewRequest(&nodev1.GetConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := config.Parse(strings.NewReader(resp.Msg.Documents))
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.Parse(strings.NewReader(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := config.Hash(docs)
	redacted, _ := config.Hash(config.Redact(want))
	if resp.Msg.Version != hashOf(t, baseConfig) || got != redacted {
		t.Errorf("config %q of version %s, want baseConfig as delivered", resp.Msg.Documents, resp.Msg.Version)
	}
	if strings.Contains(resp.Msg.Documents, "s3cret") || strings.Contains(resp.Msg.Documents, `"replicas"`) {
		t.Errorf("config = %s, want the secret hidden and no defaults", resp.Msg.Documents)
	}
	if _, err := s.GetConfig(context.Background(), connect.NewRequest(&nodev1.GetConfigRequest{Staged: true})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("staged: %v, want not found", err)
	}
}

// TestApplyConfigRetriesAFailedReload: a reload that failed is taken again when the delivery is,
// although the slot holds the delivered config already. The baseline the retry compares with is
// readable by root alone and holds the Secrets' values as their hashes; the delivery that is
// applied whole removes it.
func TestApplyConfigRetriesAFailedReload(t *testing.T) {
	s, r := configServer(t)
	next := baseConfig + networkConfig
	r.rules = []rule{{prefix: "systemctl reload systemd-networkd.service", err: errors.New("exit status 1")}}
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "reload systemd-networkd.service by hand") {
		t.Fatalf("err = %v, want the reload named", err)
	}
	info, err := os.Stat(s.Paths.EffectsBaseline)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("baseline: %v, %v; want it readable by root alone", info, err)
	}
	if b := readFile(t, s.Paths.EffectsBaseline); strings.Contains(b, "s3cret") || !strings.Contains(b, `"name":"first"`) {
		t.Errorf("baseline = %s, want the config before the delivery with its Secret hashed", b)
	}
	r.rules, r.calls = nil, nil
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.calls, "systemctl reload systemd-networkd.service") {
		t.Errorf("the retry ran %v, want networkd reloaded", r.calls)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("baseline after a delivery applied whole: %v", err)
	}
	r.calls = nil
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.calls, "systemctl reload systemd-networkd.service") {
		t.Errorf("the same config again ran %v", r.calls)
	}
}

// TestApplyConfigReplacesATornBaseline: a baseline that cannot be decoded, as one a crash cut
// short, goes to the journal, and the delivery compares with the running slot instead and
// records that as the baseline.
func TestApplyConfigReplacesATornBaseline(t *testing.T) {
	s, r := configServer(t)
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	write(t, s.Paths.EffectsBaseline, `{"documents": [`)
	renamed := strings.Replace(baseConfig, "name: first", "name: second", 1)
	r.rules = []rule{{prefix: "systemctl try-restart demo.service", err: errors.New("exit status 1")}}
	if _, err := sendConfig(s, renamed); err == nil || !strings.Contains(err.Error(), "restart demo.service by hand") {
		t.Fatalf("err = %v, want demo restarted for the change from the running slot", err)
	}
	if !strings.Contains(logged.String(), "the baseline of the effects: ") || !strings.Contains(logged.String(), "comparing with the running slot") {
		t.Errorf("log = %q, want the torn baseline named", logged.String())
	}
	if _, found, err := readBaseline(s.Paths.EffectsBaseline); err != nil || !found || !strings.Contains(readFile(t, s.Paths.EffectsBaseline), `"name":"first"`) {
		t.Errorf("baseline = %s, %v; want the running slot's config", readFile(t, s.Paths.EffectsBaseline), err)
	}
}

// TestApplyConfigRefusesWithoutABaseline: a delivery whose baseline cannot be written is refused
// before it changes anything, since a failure part way would leave a retry nothing to compare
// with.
func TestApplyConfigRefusesWithoutABaseline(t *testing.T) {
	s, r := configServer(t)
	dir := filepath.Dir(s.Paths.EffectsBaseline)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	renamed := strings.Replace(baseConfig, "name: first", "name: second", 1)
	_, err := sendConfig(s, renamed)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "record the config, facts and outputs the effects compare with") {
		t.Fatalf("err = %v, want the baseline's failure", err)
	}
	if v, err := store.Version(slotOf(t, s)); err != nil || v != hashOf(t, baseConfig) {
		t.Errorf("stored version = %q, %v; want the config from before", v, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("a refused delivery ran %v", r.calls)
	}
}

// TestApplyConfigComparesWithTheConfigBeforeAFailure: a delivery after one that failed part way
// compares with the config from before that one, not with what it applied: the Demo the failed
// delivery renamed is renamed still, so demo restarts.
func TestApplyConfigComparesWithTheConfigBeforeAFailure(t *testing.T) {
	s, r := configServer(t)
	renamed := strings.Replace(baseConfig, "name: first", "name: second", 1)
	r.rules = []rule{{prefix: "systemctl try-restart demo.service", err: errors.New("exit status 1")}}
	if _, err := sendConfig(s, renamed); err == nil {
		t.Fatal("the failed restart went unreported")
	}
	r.rules, r.calls = nil, nil
	resp, err := sendConfig(s, renamed+timeSyncConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-chrony-dhcp.service", "demo.service"}) {
		t.Errorf("restarted %v, want demo with the TimeSync's unit", resp.RestartedUnits)
	}
}

// TestApplyConfigReloadsNetworkdForUnitsThatDiffer: networkd reloads when the units the config
// loader writes differ from those it found, as after a boot whose config loader failed, although
// the networkd documents are the same.
func TestApplyConfigReloadsNetworkdForUnitsThatDiffer(t *testing.T) {
	s, r := configServer(t)
	next := baseConfig + networkConfig
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(s.Loader.NetworkDir); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.calls, "systemctl reload systemd-networkd.service") {
		t.Errorf("ran %v, want networkd reloaded for the units written again", r.calls)
	}
	if _, err := os.Stat(filepath.Join(s.Loader.NetworkDir, "10-uplink.network")); err != nil {
		t.Errorf("the unit is not written again: %v", err)
	}
}

// TestApplyConfigOverALegacySlot: a slot written before the documents were kept holds the
// identity alone, whose networkd units and time servers the node runs. Config that gives neither
// changes no networkd or TimeSync document, but the units and the sources file it writes differ,
// so networkd and chrony reload.
func TestApplyConfigOverALegacySlot(t *testing.T) {
	s, r := configServer(t)
	var id map[string]any
	if err := json.Unmarshal([]byte(identityOf(t, s)), &id); err != nil {
		t.Fatal(err)
	}
	id["networkUnits"] = map[string]any{"10-legacy.network": "[Match]\nName=enp1s0\n"}
	id["time"] = map[string]any{"servers": []any{map[string]any{"host": "legacy.ntp"}}}
	legacy, _ := json.Marshal(id)
	if err := store.Write(slotOf(t, s), store.Stored{Identity: legacy}); err != nil {
		t.Fatal(err)
	}
	if err := s.Loader.Apply(legacy); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if _, err := sendConfig(s, baseConfig); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"systemctl reload systemd-networkd.service", "chalkos-chrony-reload-sources"} {
		if !slices.Contains(r.calls, want) {
			t.Errorf("ran %v, want %s", r.calls, want)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Loader.NetworkDir, "10-legacy.network")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the legacy unit stays: %v", err)
	}
}

// demoOutput is an output of the Demo kind whose renderer and check run the shell commands given,
// owned by the test's user.
func demoOutput(t *testing.T, s *Server, render, check string) outputs.Definition {
	t.Helper()
	d := outputs.Definition{
		Inputs: []string{"Demo"},
		Render: shellScript(t, render),
		Dir:    filepath.Join(s.Loader.Render.Root, "demo"),
		User:   strconv.Itoa(os.Getuid()),
		Group:  strconv.Itoa(os.Getgid()),
		Mode:   "0644",
	}
	if check != "" {
		d.Check = shellScript(t, check)
	}
	return d
}

// shellScript writes an executable script of the shell commands given and returns its path.
func shellScript(t *testing.T, commands string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -e\n"+commands+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeOutputs declares the image's outputs at the test server's path.
func writeOutputs(t *testing.T, s *Server, defs map[string]outputs.Definition) {
	t.Helper()
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Loader.Outputs, string(data))
}

// TestApplyConfigRestartsOnChangedOutputs restarts the units of the effects whose outputs
// changed: their files, or a Secret their documents reference, as a rotated password changes no
// file. The built-in effects restart a unit by their rules.
func TestApplyConfigRestartsOnChangedOutputs(t *testing.T) {
	for name, c := range map[string]struct {
		next string
		want []string
	}{
		"unchanged":                  {baseConfig, nil},
		"a Secret a Demo references": {strings.Replace(baseConfig, "token: s3cret", "token: n3w", 1), []string{"demo.service"}},
		"a Demo":                     {strings.Replace(baseConfig, "name: first", "name: second", 1), []string{"demo.service"}},
		"a TimeSync added":           {baseConfig + timeSyncConfig, []string{"chalkos-chrony-dhcp.service"}},
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

// TestApplyConfigRefusesAnOutputThatFails: a renderer or a check that fails refuses the delivery
// before it changes anything, names the output and the exit status, and leaves what the program
// wrote to standard error, which can echo the documents, to the journal; the status records it.
func TestApplyConfigRefusesAnOutputThatFails(t *testing.T) {
	for name, tc := range map[string]struct {
		render, check, want string
	}{
		"a renderer": {`echo "rendering second" >&2; grep -q first`, "", "output demo: its renderer failed (exit 1); see the node's journal"},
		"a check":    {`cat > "$OUT/demo.json"`, `echo "rejected second" >&2; grep -q first "$OUT/demo.json"`, "output demo: its check refused the rendered files (exit 1); see the node's journal"},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, tc.render, tc.check)})
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			before := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json"))
			_, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "second") {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if !strings.Contains(logged.String(), "second") {
				t.Errorf("the journal lacks the program's stderr: %s", logged.String())
			}
			if v, _ := store.Version(slotOf(t, s)); v != hashOf(t, baseConfig) || len(r.calls) != 0 {
				t.Errorf("stored %s, ran %v", v, r.calls)
			}
			if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); got != before {
				t.Errorf("the output the node serves changed to %s", got)
			}
			if _, err := os.Stat(s.Paths.EffectsBaseline); !os.IsNotExist(err) {
				t.Errorf("a refused delivery left its baseline: %v", err)
			}
			st, err := outputs.ReadStatus(s.Loader.Render.Root)
			if err != nil || st["demo"].Error != strings.TrimPrefix(tc.want, "output demo: ") || st["demo"].Hash == "" {
				t.Errorf("status = %+v, %v", st, err)
			}
		})
	}
}

// TestApplyConfigWithoutConfigKeepsAFailingOutput: a delivery without config, such as the node's
// trust, renders the documents the node keeps, which it does not change, so an output that fails
// is recorded in the status and the journal and does not refuse the delivery.
func TestApplyConfigWithoutConfigKeepsAFailingOutput(t *testing.T) {
	s, _ := configServer(t)
	writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `echo "rendering first" >&2; exit 4`, "")})
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	before := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json"))
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Trust: []byte(s.Certificate.OSCA())})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "output demo: its renderer failed (exit 4)") {
		t.Errorf("the journal lacks the failure: %s", logged.String())
	}
	if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); got != before {
		t.Errorf("the output the node serves changed to %s", got)
	}
	st, err := outputs.ReadStatus(s.Loader.Render.Root)
	if err != nil || st["demo"].Error != "its renderer failed (exit 4); see the node's journal" || st["demo"].Hash == "" {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// TestApplyConfigSwapsOutputsAfterTheSlot: the outputs follow the stored slot, so a delivery that
// fails before its slot is written leaves the files the node serves, and a retry, which compares
// with the baseline, renders, swaps and restarts all the same.
func TestApplyConfigSwapsOutputsAfterTheSlot(t *testing.T) {
	s, r := configServer(t)
	next := strings.Replace(baseConfig, "name: first", "name: second", 1)
	slots := filepath.Dir(slotOf(t, s))
	if err := os.Chmod(slots, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot to fail", err)
	}
	if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); strings.Contains(got, "second") {
		t.Errorf("swapped the output before the slot: %s", got)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	resp, err := sendConfig(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"demo.service"}) {
		t.Errorf("restarted %v", resp.RestartedUnits)
	}
	if got := readFile(t, filepath.Join(s.Loader.Render.Root, "demo", "demo.json")); !strings.Contains(got, "second") {
		t.Errorf("the output = %s", got)
	}
}

// TestApplyConfigStartsFailedUnits: a unit that failed, such as one that found no document of its
// kind at boot, is started again with the new config, which try-restart would not do. One that
// is active or inactive is try-restarted, so a stopped unit stays stopped.
func TestApplyConfigStartsFailedUnits(t *testing.T) {
	for state, want := range map[string][]string{
		"failed":   {"systemctl reset-failed demo.service", "systemctl restart demo.service"},
		"active":   {"systemctl try-restart demo.service"},
		"inactive": {"systemctl try-restart demo.service"},
	} {
		t.Run(state, func(t *testing.T) {
			s, r := configServer(t)
			r.rules = append(r.rules, rule{prefix: "systemctl show --property=ActiveState --value demo.service", out: state + "\n"})
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

// TestApplyConfigRefusesEffectsThatDoNotCompile: the running image's effects are its own, so one
// whose rule does not compile fails the delivery before it changes anything.
func TestApplyConfigRefusesEffectsThatDoNotCompile(t *testing.T) {
	s, r := configServer(t)
	write(t, s.Loader.Effects, `{"demo": {"when": "config ==", "restart": ["demo.service"]}}`)
	_, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1))
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), `the running image's effects: effect demo: rule "config =="`) {
		t.Fatalf("err = %v", err)
	}
	if v, _ := store.Version(slotOf(t, s)); v != hashOf(t, baseConfig) || len(r.calls) != 0 {
		t.Errorf("stored %s, ran %v", v, r.calls)
	}
}

// TestStatusReportsOutputs names each output with the hash of the files the node serves and why
// its last render failed, until one passes.
func TestStatusReportsOutputs(t *testing.T) {
	s, _ := configServer(t)
	first := status(t, s).Outputs
	if len(first) != 1 || first[0].Name != "demo" || len(first[0].Hash) != 64 || first[0].Error != "" {
		t.Fatalf("outputs = %v", first)
	}
	writeOutputs(t, s, map[string]outputs.Definition{"demo": demoOutput(t, s, `cat > "$OUT/demo.json"`, `grep -q first "$OUT/demo.json"`)})
	if _, err := sendConfig(s, strings.Replace(baseConfig, "name: first", "name: second", 1)); err == nil {
		t.Fatal("the check passed")
	}
	got := status(t, s).Outputs
	if len(got) != 1 || got[0].Hash != first[0].Hash || got[0].Error != "its check refused the rendered files (exit 1); see the node's journal" {
		t.Errorf("outputs after a refused delivery = %v", got)
	}
	if _, err := sendConfig(s, strings.Replace(baseConfig, "token: s3cret", "token: n3w", 1)); err != nil {
		t.Fatal(err)
	}
	if got := status(t, s).Outputs; len(got) != 1 || got[0].Hash != first[0].Hash || got[0].Error != "" {
		t.Errorf("outputs after a delivery = %v", got)
	}
}

// TestStatusToleratesACorruptOutputStatus: a status file that cannot be read reports every
// output as not rendered yet, as the next render replaces the file.
func TestStatusToleratesACorruptOutputStatus(t *testing.T) {
	s, _ := configServer(t)
	write(t, filepath.Join(s.Loader.Render.Root, ".status.json"), "{")
	if got := status(t, s).Outputs; len(got) != 1 || got[0].Name != "demo" || got[0].Hash != "" || got[0].Error != "" {
		t.Errorf("outputs = %v", got)
	}
}
