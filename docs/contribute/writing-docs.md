---
title: "Writing documentation"
description: "How to write, build, check and preview the documentation"
---

# Writing documentation

The documentation is a [Zensical](https://zensical.org/) site built from `zensical.toml` and
the Markdown pages in `docs/`. Part of the reference is generated from the code. Checks fail when
it is out of date or when a page shows a command that does not exist, so the pages cannot drift
from what chalkos does. This page explains how the site is organised, built, generated, checked
and published. What goes into a page, and how it is worded, is the
[style guide](style.md)'s subject.

## Site sections

The navigation has one section per reader and two that every reader shares:

| Section | Directory | Reader | Kinds of page |
| --- | --- | --- | --- |
| Get started | `get-started/` | a reader new to chalkos | explanation, tutorial |
| Operate nodes | `operate/` | operator | how-to |
| Run Kubernetes | `kubernetes/` | operator running Kubernetes | how-to |
| Build images | `build/` | image author | how-to |
| How chalkos works | `how-it-works/node/`, `how-it-works/kubernetes/` | everyone | explanation |
| Reference | `reference/` | everyone | reference |
| Contribute | `contribute/` | contributor | how-to, explanation |

Each section starts with an index page with a card for each of its pages. The
[style guide](style.md#audiences) describes the readers, and the
[page templates](style.md#page-templates) describe the kinds of page.

`zensical.toml` sets the site's name, theme and navigation. The navigation lists every page;
the region between `# BEGIN docgen cli` and `# END docgen cli` is generated, and the rest is
written by hand.

## Build and preview the site

Serve the working tree while editing:

```sh
nix run .#docs-serve
```

Zensical serves a copy of `docs/` without `docs/superpowers`, which git ignores, and the copy is
kept in sync every second, so a saved page appears after a reload. Pass Zensical's own options
after `--`.

Build the site as it is published:

```sh
nix build .#docs
```

The build sees only the files git tracks, so `git add` a new page before building it. It runs
`zensical build --strict`, which fails on a link to a page or an anchor that does not exist.

## Generated pages

These pages are generated and never edited by hand:

| Page | Generated from | By |
| --- | --- | --- |
| `docs/reference/cli/` | the command trees of chalkctl, chalklab, chalkbuild and chalkcfg, with their help in `pkg/chalkctl/help.go`, `pkg/chalkctl/layers/*/help.go`, `pkg/chalklab/help.go`, `pkg/chalkbuild/help.go` and `pkg/chalkcfg/help.go` | `docgen cli` |
| `docs/reference/kinds.md` | the JSON Schemas of the default variant's kinds, declared in `modules/node/kinds/builtin/`, and of the inventory and the lab file, `pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json` and `pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json` | `docgen kinds` |
| `docs/reference/mkimages.md`, `docs/reference/image-options.md`, `docs/reference/extension-api.md` | the arguments of `mkImages` in `lib/images-arguments.nix` and the option declarations of the node modules, as nixosOptionsDoc's JSON; their openings, and the table of `lib.chalkos` that ends the extension API, in `nix/docs-templates` | `docgen options` |
| `docs/reference/builtins.md` | the schemas, `facts.json`, `outputs.json` and `effects.json` of an image of the default variant and of one with `kubernetes = false`, which the node modules in `modules/node/` declare | `docgen builtins` |
| `docs/reference/api.md` | the proto files of the node API and of each layer's API, `api/chalkos/*/v1/*.proto`, through the template `api/markdown.tmpl` | protoc-gen-doc in `buf.gen.yaml` |
| the navigation region of `zensical.toml` | the command trees | `docgen cli` |
| `includes/abbreviations.md` | the first sentence of each entry of `docs/reference/glossary.md` | `docgen abbr` |

To change one, change its source and regenerate all of them:

```sh
nix run .#docgen
```

The `docgen` app runs the script `docs.write` of `nix/docs.nix`, which copies the output of the
derivation `docs.reference` into the working tree, so it writes exactly what the check compares.
Run it in the repository's root, where `zensical.toml` is; elsewhere it refuses. Commit the source
and the generated pages together.

## Glossary tooltips

Every page shows a glossary term's definition as a tooltip when the reader hovers over the term.
`docgen abbr` turns each entry of `docs/reference/glossary.md` into lines of Python-Markdown's
`abbr` syntax in `includes/abbreviations.md`, and `pymdownx.snippets` appends that file to every
page. Terms in code get no tooltip; terms in headings and link text do.

```markdown title="docs/reference/glossary.md"
## Slot { #slot }
<!-- abbr: slots, Slots -->

A pair of partitions that holds one image's store: the erofs data and its dm-verity hash tree.
They are labelled ...
```

This entry produces one line for each form of the term. The definition is the first sentence,
with links and backticks removed:

```text
*[Slots]: A pair of partitions that holds one image's store: the erofs data and its dm-verity hash tree.
*[slots]: A pair of partitions that holds one image's store: the erofs data and its dm-verity hash tree.
*[Slot]: A pair of partitions that holds one image's store: the erofs data and its dm-verity hash tree.
*[slot]: A pair of partitions that holds one image's store: the erofs data and its dm-verity hash tree.
```

A term's forms are its heading, the forms its `<!-- abbr: ... -->` comment lists, and the heading
with a lower-case first letter. The lower-case form exists only when the heading's first word is
capitalised and no later word is, so Secure Boot gets no "secure Boot". The comment takes two
keywords:

- `nolower` drops the lower-case form. Effect has `<!-- abbr: nolower, Effects, effects -->`, so
  "take effect" gets no tooltip.
- `none` gives the term no tooltip. Use it for a term that is mostly an ordinary word: Admin,
  Config, Identity, Image, Kind, Lab, Node, Operator, Output, Reader and Upgrade have it.

The longer form wins where two overlap, so "client files" is matched before "client file", and
"config slot" before "slot". Tooltips have no underline until the pointer is over them, so they
do not clutter the text. Readers on touch screens do not see them, which is acceptable because
every term is also in the glossary.

`nix run .#docs-serve` does not regenerate the tooltips. After changing the glossary, run
`nix run .#docgen`, reload the page, and commit both files.

## Code blocks

Pick the fence by what the block holds:

| Content | Fence | Prompt |
| --- | --- | --- |
| A command with its output | `console` | `$ ` before each command; output lines without one |
| Commands without output, to run or copy | `sh` | none |
| A script file | `sh` with `title="name.sh"` | none |
| Output alone, logs, console text | `text` | none |
| Nix, JSON, YAML, TOML | `nix`, `json`, `yaml`, `toml` | none |

````markdown
```console
$ chalkctl status cp1
```
````

Output comes from a real run. Shorten long output with a line holding only `...`, and leave the
rest unchanged. Break a command longer than about 100 characters with ` \` and indent the
continuation by two spaces.

A block that is a file names it in its `title`, such as `title="nodes/w1.yaml"`. Show whole config
documents, with `apiVersion`, `kind`, `metadata.name` and `spec`, so `docgen yaml` checks them.

A value the reader replaces is a placeholder in angle brackets, such as `<node>` or `<db-key>`.
Explain each one next to the block. Tutorials use the lab's real names, `cp1` and `w1`, instead.
A whole config document uses concrete names, because `docgen yaml` refuses `<node>` as a name.
Write a placeholder flag value with `=`, as in `--fingerprint=<fingerprint>`. Without it, the
shell and `docgen check` read the `<` as a redirection and report the flag as missing its value.

```sh
chalkctl install <node> --fingerprint=<fingerprint>
```

A comment in a block explains a decision, not the next line:
`# A single control plane has no API server while it reboots.`

## Links

Links between pages use relative paths to the Markdown file, such as
`[the image](../how-it-works/node/image.md#the-root-hash-identifies-an-image)`. The strict build fails on a missing page or
anchor. The generated reference has predictable anchors:

| Target | Path and anchor |
| --- | --- |
| A command | `reference/cli/` and the command path joined with underscores: `cli/chalkctl_etcd_leave.md`; a flag is `#flag-` and its long name: `cli/chalkctl_upgrade.md#flag-image` |
| An option | its page, `mkimages.md`, `image-options.md` or `extension-api.md`, and its name in lower case, keeping only letters, digits, `-` and `_`, with `<name>` and `*` dropped: `mkimages.md#variantsmodules`, `image-options.md#chalkosdiskespsize` |
| An API method, message or enum | `method-`, `message-` or `enum-` and the name in lower case without dots: `api.md#method-upgrade` |
| A kind | its name in lower case: `kinds.md#systemdisk` |
| A built-in | `builtins.md#` and `rules-` and a kind in lower case, or `fact-`, `output-` or `effect-` and the name: `builtins.md#rules-volume`, `builtins.md#effect-networkd` |

## Checks

Two flake checks guard the documentation:

| Check | Fails when |
| --- | --- |
| `docs` | The navigation names a page that does not exist; the strict build finds a broken link or anchor; `docgen check` finds a command that does not exist; `docgen yaml` finds a config document the schemas refuse; or a Nix block a page shows does not parse. |
| `docs-generated` | The generated pages in the repository differ from what the code generates. It names `nix run .#docgen` as the fix. |

Run both before handing in a change:

```sh
nix build --no-link -L .#checks.x86_64-linux.docs .#checks.x86_64-linux.docs-generated
```

`docgen check` reads every fenced block in `sh`, `bash`, `shell`, `zsh`, `console`,
`shell-session` or without a language. It strips `$ ` prompts, joins lines ending in `\`,
follows wrappers such as `sudo`, `env`, `nix run .#chalklab --` and `nix develop -c`, and
reports a `chalkctl`, `chalklab`, `chalkbuild` or `chalkcfg` command, subcommand or flag that
does not exist, a single-dash long flag and a flag missing its value. Positional arguments and
flag values are not checked, so placeholders such as `<node>` pass. For a page whose block runs
`chalkctl upgrade` with a `--dry-run` flag and `chalkctl status` with `-insecure`, it reports:

```text
docgen: commands in the documentation that chalkctl, chalklab, chalkbuild and chalkcfg do not have:
docs/operate/example.md:8: chalkctl upgrade --config lab.json --dry-run: chalkctl upgrade has no flag --dry-run
docs/operate/example.md:9: chalkctl status cp1 -insecure: chalkctl status has no flag -i
```

Commands shown in prose or in a `text` block are not checked, so show commands in shell blocks.
To run the check alone, from the repository's root:

```sh
nix develop -c go run ./cmd/docgen check docs
```

`docgen yaml` reads every `yaml` block and checks each document whose group is `chalkos.dev`, a
subdomain of it, or `rack.example.org`, the homelab's extension, as `chalkctl validate` checks a
node's files: against the schemas of an image of the default variant with the homelab's `Rack`
kind, and of the inventory and the lab file. A kind the image does not declare, a misspelled field
or a value of the wrong type fails, and the error names the page's line the document starts on.
Documents of other groups, such as Kubernetes objects, fragments without an `apiVersion` and
documents that are not objects are not checked.

The `docs` check also runs nixfmt's parser over every `nix` block. A block whose `title` names a
`.nix` file is a whole file and must parse as one. A block without such a title is a fragment and
must parse as an expression, or within braces as the attributes of a module, such as
`chalkos.kernel.moduleGroups = [ "gpu" ];`. Both checks run only in the `docs` check, which
builds the image's schemas and provides nixfmt.

## Add a page

1. Create the Markdown file in its section's directory, starting with front matter of a `title`
   and a `description` and an H1 equal to the title, as the
   [style guide](style.md#front-matter-and-file-names) shows.
2. Write it from the template of its kind in the [style guide](style.md#page-templates).
3. Add it to the navigation in `zensical.toml`, outside the generated region, and to its
   section's index page.
4. `git add` it, then run the `docs` check.

## Publishing

The workflow `.github/workflows/docs.yml` builds `.#docs` on every push to `main` and deploys
the result to GitHub Pages, at <https://trevex.github.io/chalkos/>. It runs no other check, so a
change is checked locally before it is merged. The documentation is not versioned: the site
describes `main`.
