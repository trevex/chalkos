# Reference

These pages are generated from the code, so they describe what the code does; `nix run .#docgen`
writes them, and the `docs-generated` check fails when they are out of date.

- [Command line](cli/index.md): every command of `chalkctl` and `chalklab`, from their help.
- [Cluster and node options](options.md): the options of a cluster definition and of role images.
- [Node API](api.md): the API chalkd serves on every node.

!!! note "Being written"

    The hand-written reference pages will describe the image and partition layout, ports and the
    firewall, and the fields chalkos adds to os-release.
