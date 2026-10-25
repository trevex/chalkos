package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOptions(t *testing.T) {
	dir := t.TempDir()
	if err := writeOptions(dir, "testdata/options/templates", "testdata/options/arguments.json", "testdata/options/options.json"); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	pages := map[string]struct{ order, want []string }{
		"mkimages.md": {
			order: []string{
				"---\ntitle: \"mkimages\"\n---\n\n# mkimages\n\nThe head of mkimages.\n",
				"\n## mkImages arguments { #mkimages-arguments }\n\n",
				"\n### `nixpkgs` { #nixpkgs }\n",
				"\n### `osCA` { #osca }\n",
				"\n### `variants` { #variants }\n",
				"\nSources: ",
			},
			want: []string{
				// A literalMD default is Markdown, an expression is code, and a description keeps its paragraphs.
				"### `nixpkgs` { #nixpkgs }\n\nType: any · Default: the nixpkgs chalkos is tested against · Declared in: [lib/images-arguments.nix](https://example.org/lib/images-arguments.nix)\n\nnixpkgs the images are built from.\n",
				"### `osCA` { #osca }\n\nType: path or null · Default: `null` · Declared in: [lib/images-arguments.nix](https://example.org/lib/images-arguments.nix)\n\nPublic part of the cluster's secrets.\n\nnull builds open images.\n\nExample: `./secrets.pub.json`\n",
				// A default over several lines shows on one, and a submodule's options are rows of a table.
				"Type: map of submodule · Default: `{ node = { }; }` · Declared in:",
				"| Option | Type | Default | Description |\n| --- | --- | --- | --- |\n| `<name>.kubernetes`{ #variantskubernetes } | boolean | `true` | Whether the variant's images run Kubernetes. |\n",
			},
		},
		"image-options.md": {
			order: []string{
				"# image-options\n\nThe head of image-options.\n",
				"\n## Disk { #disk }\n\n",
				"\n### `chalkos.disk.espSize` { #chalkosdiskespsize }\n",
				"\n## Kernel { #kernel }\n\n",
				"\n## Kubernetes and CNI { #kubernetes-and-cni }\n\n",
				"\n### `chalkos.kubernetes.enable` { #chalkoskubernetesenable }\n",
				"\n### `chalkos.cni.provider` { #chalkoscniprovider }\n",
				"\n## Router and forwarding { #router-and-forwarding }\n\n",
				"\n### `chalkos.router.enable` { #chalkosrouterenable }\n",
				"\n## Upgrade and health { #upgrade-and-health }\n\n",
				"\n## Debug and maintenance { #debug-and-maintenance }\n\n",
				"\n## Set by mkImages { #set-by-mkimages }\n\n",
				"\n### `chalkos.variant.name` { #chalkosvariantname }\n",
				"\nSources: ",
			},
			want: []string{
				"### `chalkos.disk.espSize` { #chalkosdiskespsize }\n\nType: string · Default: `\"1G\"` · Declared in: [modules/node/base/disk.nix](https://example.org/modules/node/base/disk.nix)\n\nSize of the EFI system partition.\n",
				"Type: one of `flannel`, `none` · Default: `\"flannel\"` ·",
				"Type: list of one of `gpu`, `sound` ·",
				"Type: integer of at least 1 ·",
				"Type: string or null · Default: `null` ·",
			},
		},
		"extension-api.md": {
			order: []string{
				"# extension-api\n\nThe head of extension-api.\n",
				"\n## Kinds { #kinds }\n\n",
				"\n### `chalkos.kinds` { #chalkoskinds }\n",
				"\n### `systemd.services.<name>.chalkos.kinds` { #systemdserviceschalkoskinds }\n",
				"\n## Facts { #facts }\n\n",
				"\n## Outputs { #outputs }\n\n",
				"\n## Effects { #effects }\n\n",
				"\n## lib.chalkos { #lib-chalkos }\n\nThe helpers.\n",
				"\nSources: ",
			},
			want: []string{
				// Read-only options say so, required ones have "required", and an example that
				// fits on a line is in its row; one that does not follows the table.
				"| `<name>.dir`{ #chalkoskindsdir } | string, read-only | `\"/run/chalkos/config/${group}/<kind>\"` | Directory of the kind's documents. |\n" +
					"| `<name>.group`{ #chalkoskindsgroup } | string matching `[a-z]+(\\.[a-z]+)+` | required | API group of the kind. Example: `\"bgp.example.org\"` |\n" +
					"| `<name>.rules`{ #chalkoskindsrules } | list of submodule | `[ ]` | Change rules of the kind. |\n" +
					"| `<name>.rules.*.takes`{ #chalkoskindsrulestakes } | one of `refuse`, `reset` | `\"refuse\"` | What a refused change needs. |\n\n" +
					"Example of `<name>.rules`:\n\n```nix\n[\n  # A rule.\n  { rule = \"true\"; message = \"never\"; }\n]\n```\n",
				"| `<name>.render`{ #chalkosoutputsrender } | command, package or lib.chalkos.render value | required | The renderer. |\n",
				"Type: list of string · Default: `[ ]` · Declared in: [modules/node/runtime/default.nix](https://example.org/modules/node/runtime/default.nix)\n\nConfig kinds the unit reads.\n\nExample: `[ \"Peer\" ]`\n",
			},
		},
	}
	for name, p := range pages {
		got := read(name)
		last := -1
		for _, h := range p.order {
			i := strings.Index(got, h)
			if i <= last {
				t.Errorf("%s: %q at %d, after %d", name, h, i, last)
			}
			last = i
		}
		for _, w := range p.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
		if strings.Contains(got, "*Type") || strings.Contains(got, "_Type") {
			t.Errorf("%s: italics", name)
		}
	}
}

func TestWriteOptionsUnmapped(t *testing.T) {
	dir := t.TempDir()
	options := filepath.Join(dir, "options.json")
	data, err := os.ReadFile("testdata/options/options.json")
	if err != nil {
		t.Fatal(err)
	}
	extra := strings.Replace(string(data), "{\n", "{\n  \"chalkos.new.thing\": {\"declarations\": [], \"description\": \"New.\", \"loc\": [\"chalkos\", \"new\", \"thing\"], \"readOnly\": false, \"type\": \"string\"},\n", 1)
	if err := os.WriteFile(options, []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	err = writeOptions(dir, "testdata/options/templates", "testdata/options/arguments.json", options)
	if err == nil || !strings.Contains(err.Error(), "chalkos.new.thing") {
		t.Errorf("an option no section holds: %v", err)
	}
}

func TestOptionAnchor(t *testing.T) {
	for name, want := range map[string]string{
		"chalkos.disk.espSize":                           "chalkosdiskespsize",
		"variants.<name>.kubernetes":                     "variantskubernetes",
		"chalkos.kinds.<name>.rules.*.messageExpression": "chalkoskindsrulesmessageexpression",
		"systemd.services.<name>.chalkos.kinds":          "systemdserviceschalkoskinds",
		"installer.modules":                              "installermodules",
	} {
		if got := optionAnchor(name); got != want {
			t.Errorf("optionAnchor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestOptionType(t *testing.T) {
	for in, want := range map[string]string{
		"string":                                                 "string",
		"null or string":                                         "string or null",
		"null or absolute path":                                  "path or null",
		"null or non-empty string":                               "non-empty string or null",
		"list of string":                                         "list of string",
		"attribute set of raw value":                             "map of any",
		"attribute set of (submodule)":                           "map of submodule",
		"list of (submodule)":                                    "list of submodule",
		`one of "flannel", "none"`:                               "one of `flannel`, `none`",
		`null or one of "restart", "reload"`:                     "one of `restart`, `reload` or null",
		`list of value "fallbackSecret" (singular enum)`:         "list of `fallbackSecret`",
		"positive integer, meaning >0":                           "integer of at least 1",
		"function that evaluates to a(n) string":                 "function returning string",
		`null or strings concatenated with "\n"`:                 "string or null",
		"string matching the pattern 0?[0-7]{3}":                 "string matching `0?[0-7]{3}`",
		"null or string matching the pattern [a-z0-9][a-z0-9-]*": "string matching `[a-z0-9][a-z0-9-]*` or null",
		"(list of string matching the pattern [a-z0-9][a-z0-9-]*) or attribute set of list of module": "list of string matching `[a-z0-9][a-z0-9-]*` or map of list of module",
		"command, package or lib.chalkos.render value":                                                "command, package or lib.chalkos.render value",
	} {
		if got := optionType(in); got != want {
			t.Errorf("optionType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTableCell(t *testing.T) {
	for in, want := range map[string]string{
		"a | b":                   `a \| b`,
		"`(alpha|beta)` or a | b": "`(alpha|beta)` or a \\| b",
		"``a ` | b`` |":           "``a ` | b`` \\|",
		"unclosed ` | pipe":       "unclosed ` \\| pipe",
		// A placeholder outside a code span would be taken for HTML; one inside shows as it is.
		"below <path>/v2, as `<path>`": "below &lt;path>/v2, as `<path>`",
		"a < b, </p>":                  "a < b, &lt;/p>",
	} {
		if got := tableCell(in); got != want {
			t.Errorf("tableCell(%q) = %q, want %q", in, got, want)
		}
	}
}
