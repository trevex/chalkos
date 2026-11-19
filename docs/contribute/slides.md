---
title: "Talks and slides"
description: "The slide decks contributed to chalkos, where the site serves them and how to add one"
---

# Talks and slides

A slide deck is a single self-contained HTML file, `index.html`, in its own directory below
`docs/slides/`. The site serves each deck as it is, and a README next to it in the repository
explains how to view, edit and export the deck.

## Decks

| Deck | Talk | README |
| --- | --- | --- |
| [chalkos: build your own API-driven, immutable Linux](../slides/chalkos-intro/index.html) | A short technical talk on the motivation, a node's anatomy and trust chain, config kinds, config delivery, upgrades, layers and tests, with an air-gapped home lab demo. | [docs/slides/chalkos-intro/README.md](https://github.com/trevex/chalkos/blob/main/docs/slides/chalkos-intro/README.md) |

## Add a deck

1. Create a directory below `docs/slides/` named after the talk, such as
   `docs/slides/chalkos-intro/`.
2. Put the deck in it as `index.html`, with its CSS and JavaScript inline. Assets the deck loads by
   a relative path go in the same directory, and the site serves them too.
3. Add a `README.md` that says what the talk covers and how to view, edit and export the deck. The
   site leaves the README out, so the repository is where it is read.
4. Add a row for the deck to the table on this page, with a link to `index.html` and one to the
   README on GitHub.

The site serves `docs/slides/<name>/index.html` at `slides/<name>/`.
[Build the site](writing-docs.md#build-and-preview-the-site) to see the deck as it is published:
the build sees only the files git tracks, so `git add` the deck first.
