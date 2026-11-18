# BIRD BGP extension

This example accompanies [Write a BGP extension](../../docs/guides/bgp-extension.md).
It builds a Kubernetes node image with BIRD and the extension's `Speaker` and `Peer`
config kinds. The extension renders and checks `bird.conf`, reloads BIRD on config
changes, and exposes the session state through a diagnostic.

From this directory, run the tests and build the example image.

```sh
nix flake check
nix build .#node-bird-metal
```

The guide shows how to build the extension from an empty module and try it on a node
using the homelab's router and cluster.
