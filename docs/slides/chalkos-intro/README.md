# chalkos intro slides

A 16-slide deck that introduces chalkos in a short technical talk: the motivation, the anatomy and
trust chain of a node, config kinds in the image, outputs and effects, config delivery, upgrades,
the layers and the tests, and an outline of the air-gapped home lab demo. The deck is one
self-contained HTML file, `index.html`, with its CSS and JavaScript inline. It loads its fonts from
Google Fonts and needs no build step.

The documentation site serves the deck at
<https://trevex.github.io/chalkos/slides/chalkos-intro/>, and lists it on the
[Talks and slides](../../contribute/slides.md) page. The site leaves this README out:
`zensical.toml` excludes `slides/**/README.md`.

## View the deck

Open `index.html` in a browser, or serve the directory and open the address the server prints:

```sh
nix run nixpkgs#python3 -- -m http.server --directory docs/slides/chalkos-intro 8000
```

The slides are a fixed 1920×1080 stage that scales to the window, so they keep 16:9 on any screen.
The address carries the slide number, as in `index.html#5`; `index.html?slide=5` opens slide 5 too.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| Right arrow, Down arrow, Page Down, Space | Next slide |
| Left arrow, Up arrow, Page Up | Previous slide |
| Home, End | First and last slide |
| E | Enter or leave edit mode |
| Esc | Leave edit mode |
| Ctrl+S (Cmd+S on macOS) | Download the edited deck as `index.html` |

A horizontal swipe and the mouse wheel also change slides.

## Edit the deck

For small text changes, press E, or hover the top-left corner and click Edit, and then click any
text to change it. The browser keeps the changes in its local storage until Ctrl+S downloads the
edited file; replace `index.html` with the download. For anything else, edit `index.html`
directly: each slide is a `<section class="slide">`, and the colours and fonts are CSS variables in
`:root`.

## Speaker notes

Each slide carries its speaker notes in a hidden `<aside class="notes" hidden>` element at the top
of its `<section>`: the details the slide leaves out but the talk may mention. The deck never shows
them; read them in the HTML source.

## Export to PDF

Headless Chromium prints one 1920×1080 page per slide:

```sh
cd docs/slides/chalkos-intro
nix run nixpkgs#chromium -- --headless --no-pdf-header-footer \
  --virtual-time-budget=5000 --print-to-pdf=chalkos-intro.pdf "file://$PWD/index.html"
```

The PDF shows each slide in its final state, without animations. Fonts come from Google Fonts, so
the export needs network access.
