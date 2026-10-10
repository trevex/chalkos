# Contributing

!!! note "Being written"

    This section will hold the contributing guide; the development setup with the dev shell and
    the repository layout; testing with unit, namespace, evaluation and end-to-end tests and the
    end-to-end tests' resource budget; the architecture for developers; writing documentation,
    with the style guide; and the roadmap and known limits.

The site is built with [Zensical](https://zensical.org): `nix build .#docs` builds it strictly,
as the `docs` check does, and `nix run .#docs-serve` serves it from the repository root while
it is edited.
