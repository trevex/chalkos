package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestFillCards(t *testing.T) {
	pages := map[string][2]string{
		"install.md":       {"Install a node", "Boot the installer and install a node"},
		"../guides/lab.md": {"A lab", "Run a lab"},
	}
	lookup := func(page string) (string, string, error) {
		p, ok := pages[page]
		if !ok {
			return "", "", fmt.Errorf("%s does not exist", page)
		}
		return p[0], p[1], nil
	}
	text := "# Operate\n\n<!-- docgen cards: install.md ../guides/lab.md -->\nstale\n<!-- end docgen cards -->\n"
	got, err := fillCards(text, lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Operate\n\n<!-- docgen cards: install.md ../guides/lab.md -->\n" +
		"<div class=\"grid cards\" markdown>\n\n" +
		"-   __[Install a node](install.md)__\n\n    ---\n\n    Boot the installer and install a node\n\n" +
		"-   __[A lab](../guides/lab.md)__\n\n    ---\n\n    Run a lab\n\n" +
		"</div>\n<!-- end docgen cards -->\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	// Filling is idempotent.
	if again, err := fillCards(got, lookup); err != nil || again != got {
		t.Errorf("filling again changed the page or failed: %v", err)
	}

	if _, err := fillCards("<!-- docgen cards: missing.md -->\n<!-- end docgen cards -->\n", lookup); err == nil {
		t.Error("a missing page passed")
	}
	for _, grid := range []string{
		`<div class="grid cards" markdown>`,
		`<div class='grid cards' markdown>`,
		`<div markdown class="cards grid">`,
	} {
		_, err = fillCards(text+"\n"+grid+"\n</div>\n", lookup)
		if err == nil || !strings.Contains(err.Error(), "by hand") {
			t.Errorf("a hand-written grid %s passed: %v", grid, err)
		}
	}
}
