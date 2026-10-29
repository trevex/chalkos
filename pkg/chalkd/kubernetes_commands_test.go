package chalkd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// TestContainerdHosts prints each registry's hosts.toml from the node's documents, in order of
// the directories, each after the line naming it.
func TestContainerdHosts(t *testing.T) {
	dir := t.TempDir()
	docs, err := config.Parse(strings.NewReader(`apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lan}
spec: {host: "10.0.0.5:5000", insecure: true}
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: hub}
spec: {registry: docker.io, endpoints: ["http://10.0.0.5:5000"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.WriteRuntime(dir, docs, nil, nil); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := containerdHosts(&out, dir); err != nil {
		t.Fatal(err)
	}
	want := "=== 10.0.0.5:5000\nserver = \"http://10.0.0.5:5000\"\n=== docker.io\nserver = \"https://registry-1.docker.io\"\n\n[host.\"http://10.0.0.5:5000\"]\n  capabilities = [\"pull\", \"resolve\"]\n"
	if out.String() != want {
		t.Errorf("containerd-hosts printed %q, want %q", out.String(), want)
	}
}

// TestRegistryCredentials writes the kubelet's credential provider files from the node's Registry
// documents, with the password from the unit's credentials, and no password into the files
// the kubelet's config and flags are.
func TestRegistryCredentials(t *testing.T) {
	runDir, creds, out := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "credential-provider")
	docs, err := config.Parse(strings.NewReader(`apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: hub}
spec: {host: docker.io, username: hub-bot, password: {secretRef: {name: hub, key: token}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.WriteRuntime(runDir, docs, nil, nil); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(creds, "secret_hub.token"), "hub-s3cret")
	if err := registryCredentials(runDir, creds, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "credentials.json"))
	if want := `{"docker.io":{"username":"hub-bot","password":"hub-s3cret"}}`; err != nil || string(data) != want {
		t.Errorf("credentials.json = %s, %v; want %s", data, err, want)
	}
	for _, f := range []string{"config.json", "flags"} {
		if data, err := os.ReadFile(filepath.Join(out, f)); err != nil || strings.Contains(string(data), "hub-s3cret") {
			t.Errorf("%s = %s, %v", f, data, err)
		}
	}
}

// TestKubernetesCommandsRefuseArguments refuses, before running anything, the arguments a
// command of the layer does not take.
func TestKubernetesCommandsRefuseArguments(t *testing.T) {
	cmds := (&kubernetesLayer{}).Commands()
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"prepare", []string{"--vxlan"}},
		{"prepare", []string{"--vxlan-rule", "x"}},
		{"vxlan-rule", []string{"a", "b"}},
		{"containerd-hosts", []string{"x"}},
		{"registry-credentials", []string{"x"}},
		{"credential-provider", []string{"x"}},
	} {
		if _, ok := errors.AsType[layer.UsageError](cmds[tc.command].Run(tc.args)); !ok {
			t.Errorf("%s %q ran", tc.command, tc.args)
		}
	}
}
