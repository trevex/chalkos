package chalkctl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
)

func TestPasswordFileTrimsOneNewline(t *testing.T) {
	ta := newTestApp(t)
	tg := &target{cluster: &cluster{name: "lab"}, name: "n1", secrets: ta.secrets}
	for content, want := range map[string]string{
		"pw\n": "pw", "pw \n\n": "pw \n", "pw": "pw",
		"pw\r\n": "pw", "pw\r": "pw", "pw\r\r\n": "pw\r", "pw\n\r\n": "pw\n",
	} {
		path := filepath.Join(ta.dir, "password")
		writeFile(t, path, content)
		if got, err := ta.fallbackSecret(context.Background(), tg, "password", path); err != nil || got != want {
			t.Errorf("password from %q = %q, %v, want %q", content, got, err, want)
		}
	}
}

func TestPasswordPromptConfirms(t *testing.T) {
	ta := newTestApp(t)
	tg := &target{cluster: &cluster{name: "lab"}, name: "n1", secrets: ta.secrets}

	ta.answers = []string{"secret one", "secret one"}
	if got, err := ta.fallbackSecret(context.Background(), tg, "password", ""); err != nil || got != "secret one" {
		t.Errorf("password = %q, %v", got, err)
	}
	if len(ta.prompts) != 2 {
		t.Errorf("prompts = %q, want the password asked twice", ta.prompts)
	}

	ta.prompts, ta.answers = nil, []string{"secret one", "secret on"}
	if _, err := ta.fallbackSecret(context.Background(), tg, "password", ""); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("err = %v, want the typo noticed", err)
	}

	ta.prompts, ta.answers = nil, nil
	if _, err := ta.fallbackSecret(context.Background(), tg, "password", ""); err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("err = %v, want --password-file suggested without a terminal", err)
	}
}

// withPassword gives n1 the password fallback and the volume data, encrypted as given.
func withPassword(t *testing.T, ta *testApp, encryption string) {
	ta.writeNode(t, passwordSystemDisk, n1Network,
		"apiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\nspec: {size: 1G, format: ext4, mountPoint: /srv/data, encryption: {mode: "+encryption+"}}\n")
}

func TestFallbackNeeded(t *testing.T) {
	section := storage.Section{Fallback: "password", Volumes: map[string]storage.Volume{
		"var":   {Encryption: storage.EncryptionTPM2},
		"data":  {Encryption: storage.EncryptionTPM2},
		"cache": {Encryption: storage.EncryptionNone},
	}}
	for _, c := range []struct {
		existing []string
		reset    string
		want     bool
	}{
		{[]string{"var", "data", "cache"}, "", false},
		{[]string{"var", "cache"}, "", true},
		{[]string{"var", "data"}, "", false},
		{[]string{"var", "data", "cache"}, "data", true},
		{[]string{"var", "data", "cache"}, "cache", false},
	} {
		if got := fallbackNeeded(section, c.existing, c.reset); got != c.want {
			t.Errorf("fallbackNeeded(existing %v, reset %q) = %v, want %v", c.existing, c.reset, got, c.want)
		}
	}
	section.Fallback = storage.FallbackNone
	if fallbackNeeded(section, nil, "data") {
		t.Error("needed a secret for a node without fallback")
	}
}
