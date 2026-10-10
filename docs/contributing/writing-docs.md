---
title: "Writing documentation"
description: "How to write, build, check and preview the documentation"
---

# Writing documentation

The documentation is a [Zensical](https://zensical.org/) site built from `zensical.toml` and
the Markdown pages in `docs/`. Part of the reference is generated from the code, and checks fail
when it is out of date or when a page shows a command that does not exist, so the pages cannot
drift from what chalkos does. This page explains how the site is organised, built, generated,
checked and published. What goes into a page, and how it is worded, is the
[style guide](style.md)'s subject.

## How is the site organised?

The navigation follows the four kinds of page the style guide defines, each with its own
section and template:

| Section | Kind | Reader's question |
| --- | --- | --- |
| Getting started | tutorial | How do I get a cluster running? |
| Concepts | concept | How does this work, and why? |
| Guides | guide | How do I do this task? |
| Reference | reference | What exactly is this value, port or field? |

Contributing, this section, holds the pages for developers. Each section starts with an index
page that lists its pages. The [glossary](../reference/glossary.md) defines the terms every page
links on first use.

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
| `docs/reference/cli/` | the command trees of chalkctl and chalklab, with their help in `pkg/chalkctl/help.go` and `pkg/chalklab/help.go` | `docgen cli` |
| `docs/reference/options.md` | the option declarations of `modules/cluster` and of the node modules | nixosOptionsDoc |
| `docs/reference/api.md` | `api/chalkos/node/v1/node.proto`, through the template `api/markdown.tmpl` | protoc-gen-doc in `buf.gen.yaml` |
| the navigation region of `zensical.toml` | the command trees | `docgen cli` |

To change one, change its source and regenerate all of them:

```sh
nix run .#docgen
```

`docgen` is a Nix derivation, `docs.reference` in `nix/docs.nix`, so the command writes exactly
what the check compares. Commit the source and the generated pages together.

## Checks

Two flake checks guard the documentation:

| Check | Fails when |
| --- | --- |
| `docs` | The navigation names a page that does not exist; the strict build finds a broken link or anchor; or `docgen check` finds a command that does not exist. |
| `docs-generated` | The generated pages in the repository differ from what the code generates. It names `nix run .#docgen` as the fix. |

Run both before handing in a change:

```sh
nix build --no-link -L .#checks.x86_64-linux.docs .#checks.x86_64-linux.docs-generated
```

`docgen check` reads every fenced block in `sh`, `bash`, `shell`, `zsh`, `console`,
`shell-session` or without a language. It strips `$ ` prompts, joins lines ending in `\`,
follows wrappers such as `sudo`, `env`, `nix run .#chalklab --` and `nix develop -c`, and
reports a `chalkctl` or `chalklab` command, subcommand or flag that does not exist, a
single-dash long flag and a flag missing its value. Positional arguments and flag values are not
checked, so placeholders such as `<node>` pass. For a page whose block runs `chalkctl upgrade`
with a `--dry-run` flag and `chalkctl status` with `-insecure`, it reports:

```text
docgen: commands in the documentation that chalkctl and chalklab do not have:
docs/guides/example.md:8: chalkctl upgrade --config lab.json --dry-run: chalkctl upgrade has no flag --dry-run
docs/guides/example.md:9: chalkctl status cp1 -insecure: chalkctl status has no flag -i
```

Commands shown in prose or in a `text` block are not checked, so show commands in shell blocks.
To run the check alone, from the repository's root:

```sh
nix develop -c go run ./cmd/docgen check docs
```

## Add a page

1. Create the Markdown file in its section's directory, starting with front matter of a `title`
   and a `description` and an H1 equal to the title, as the
   [style guide](style.md#front-matter) shows.
2. Write it from the template of its kind in the [style guide](style.md#page-templates).
3. Add it to the navigation in `zensical.toml`, outside the generated region, and to its
   section's index page.
4. `git add` it, then run the `docs` check.

## Publishing

The workflow `.github/workflows/docs.yml` builds `.#docs` on every push to `main` and deploys
the result to GitHub Pages, at <https://trevex.github.io/chalkos/>. It runs no other check, so a
change is checked locally before it is merged. The documentation is not versioned: the site
describes `main`.
