package chalkctl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/storage"
)

func TestApplyConfigAndStatus(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "config none (not the inventory's; chalkctl apply-config n1 delivers it)") || !strings.Contains(ta.stdout.String(), "var") {
		t.Errorf("status before = %q", ta.stdout)
	}

	ta.stdout.Reset()
	r.calls = nil
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	// The node's slot held no documents, so the config's Network is new: the units of the default
	// image that read the node's static addresses restart. The node declares no outputs, so the
	// effect on networkd's output does not fire.
	if !reflect.DeepEqual(r.calls, []string{
		"systemctl list-units --state=failed --plain --no-legend --no-pager", "chronyc -n -c tracking",
		"systemctl show --property=ActiveState --value chalkos-kubernetes.service",
		"systemctl show --property=ActiveState --value kubelet.service",
		"systemctl restart chalkos-kubernetes.service kubelet.service",
	}) {
		t.Errorf("node ran %v", r.calls)
	}
	want := ta.configVersion(t)
	if v, err := slot.Version(slotOf(t, s)); err != nil || v != want {
		t.Errorf("config version on the node = %q, %v; want %s", v, err, want)
	}
	if !strings.Contains(ta.stdout.String(), "n1 runs config "+want) {
		t.Errorf("stdout = %q", ta.stdout)
	}

	ta.stdout.Reset()
	r.calls = []string{}
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "platform metal (the inventory's)") || !strings.Contains(ta.stdout.String(), "config "+want+" (the inventory's)") {
		t.Errorf("status after = %q", ta.stdout)
	}
}

// apply-config prints why the rule of an effect could not be evaluated as a warning, without the
// values it read; the node took the rule's effect all the same.
func TestApplyConfigWarnsOfRulesThatFail(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	var effects map[string]any
	raw, err := os.ReadFile(s.Loader.Effects)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &effects); err != nil {
		t.Fatal(err)
	}
	effects["report"] = map[string]any{"when": `config["chalkos.dev/v1alpha1"].Cluster.missing.spec.endpoint == ""`, "restart": []string{"report.service"}}
	data, _ := json.Marshal(effects)
	writeFile(t, s.Loader.Effects, string(data))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "chalkctl: warning: n1: the rule of effect report: a rule cannot be evaluated: no such key; taking the effect all the same\n"
	if ta.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", ta.stderr, want)
	}
	if !slices.Contains(r.calls, "systemctl try-restart report.service") {
		t.Errorf("node ran %v, want report restarted", r.calls)
	}
}

func TestApplyConfigNamesTheRemedy(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	// The node recorded VAR as xfs, so the inventory's ext4 is a destructive change, which only a
	// reinstall carries out, since a reset cannot recreate VAR while the node runs.
	section := ta.identity(t).Storage
	v := section.Volumes["var"]
	v.Format = "xfs"
	section.Volumes = map[string]storage.Volume{"var": v}
	data, _ := json.Marshal(section)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(data))
	addr := ta.startNode(t, s)

	err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "only a reinstall of n1 carries it out") {
		t.Fatalf("err = %v, want a reinstall of n1", err)
	}
}

func TestApplyConfigAsksForPasswordOnlyForNewEncryptedVolumes(t *testing.T) {
	ta := newTestApp(t)
	ta.writeNode(t, passwordSystemDisk, n1Network)
	s, r := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)

	// Nothing to enroll: the password is not asked for.
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if len(ta.prompts) != 0 {
		t.Errorf("asked %q for an identity that enrolls nothing", ta.prompts)
	}

	// The new encrypted volume gets the password: it is asked twice, and a typo stops the
	// command before the identity is sent.
	withPassword(t, ta, storage.EncryptionTPM2)
	r.calls = nil
	ta.answers = []string{"right", "wrong"}
	err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("err = %v, want the typo noticed", err)
	}
	if len(ta.prompts) != 2 || !reflect.DeepEqual(r.calls, []string{"systemctl list-units --state=failed --plain --no-legend --no-pager", "chronyc -n -c tracking"}) {
		t.Errorf("prompts = %q, node ran %v", ta.prompts, r.calls)
	}
}

func TestStorageResetAsksForPasswordOnlyForEncryptedVolume(t *testing.T) {
	for _, c := range []struct {
		encryption string
		prompts    int
		// want is why the reset fails: the node has no partition of volume data yet, or the password
		// typed twice differs.
		want string
	}{{storage.EncryptionNone, 0, "volume data has no recorded partition"}, {storage.EncryptionTPM2, 2, "differ"}} {
		t.Run(c.encryption, func(t *testing.T) {
			ta := newTestApp(t)
			withPassword(t, ta, c.encryption)
			s, _ := installedNode(t, ta, []byte(`{}`))
			addr := ta.startNode(t, s)
			ta.answers = []string{"right", "wrong"}
			err := ta.run(context.Background(), ta.args([]string{"storage", "reset", "n1", "data"}, addr))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("reset = %v, want %q", err, c.want)
			}
			if len(ta.prompts) != c.prompts {
				t.Errorf("prompts = %q, want %d", ta.prompts, c.prompts)
			}
		})
	}
}

// TestApplyConfigRefusesAnotherPlatform refuses to deliver config to a node whose image was built
// for another platform than its image's: the node would translate the config
// for its own.
func TestApplyConfigRefusesAnotherPlatform(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=test\nCHALKOS_PLATFORM=kvm\n")
	addr := ta.startNode(t, s)
	want := "n1 runs an image built for kvm, but its image test-metal is built for metal; changing a node's platform is a reinstall"
	for _, cmd := range [][]string{{"apply-config", "n1"}, {"storage", "reset", "n1", "var"}} {
		err := ta.run(context.Background(), ta.args(cmd, addr))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s = %v, want %q", cmd[0], err, want)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("node ran %v for refused config", r.calls)
	}
}

// TestApplyConfigToAnImageOfAnotherVariant delivers config to a node that runs an image of another
// variant than its image's: the node checks the config against its image's schemas.
func TestApplyConfigToAnImageOfAnotherVariant(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=w\nCHALKOS_PLATFORM=metal\n")
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := ta.configVersion(t)
	if v, err := slot.Version(slotOf(t, s)); err != nil || v != want {
		t.Errorf("config version on the node = %q, %v; want %s", v, err, want)
	}
}

// TestApplyConfigStopsOnAFailingCheck sends nothing when the documents do not check against the
// schemas of the node's image: here the image lacks networkd's Network kind.
func TestApplyConfigStopsOnAFailingCheck(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	s.Loader.Schemas = filepath.Join(t.TempDir(), "schemas")
	entries, err := os.ReadDir(fixtureSchemas[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(fixtureSchemas[0], e.Name()))
		writeFile(t, filepath.Join(s.Loader.Schemas, e.Name()), string(data))
	}
	addr := ta.startNode(t, s)
	err = ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "the config of n1") {
		t.Fatalf("apply-config = %v, want the check's refusal", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("node ran %v", r.calls)
	}
	if got, _, _ := slot.Read(slotOf(t, s)); string(got.Identity) != `{"hostname": "old"}` {
		t.Errorf("identity on the node = %s", got)
	}
	if v, err := slot.Version(slotOf(t, s)); err != nil || v != "" {
		t.Errorf("config version on the node = %q, %v; want none", v, err)
	}
}
