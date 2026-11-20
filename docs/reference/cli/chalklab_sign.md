---
title: "chalklab sign"
description: "Sign an image with the lab's Secure Boot keys, for upgrades"
---

# chalklab sign

```sh
chalklab sign <image> [flags]
```

Signs the boot loader and UKIs of an image with the lab's Secure Boot db key, so the lab's VMs
boot it; an upgrade with an image built elsewhere needs that. An image in the Nix store cannot be
changed, so --out copies the image directory, with its schemas and metadata, into a new directory
and signs the copy, which chalkctl upgrade --image takes. An image in a registry is signed in the
directory chalkctl image pull writes. The command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--name`{ #flag-name } | string | the only lab | Lab whose keys to sign with. |
| `--out`{ #flag-out } | string | — | New directory to copy the image directory to, with its schemas and metadata, and sign there; needed for an image in the Nix store, which cannot be signed in place. |

## Examples

```sh
# Sign a copy of an image in the Nix store and upgrade the lab's nodes of its name with it,
# with $CHALKOSINVENTORY and $CHALKOSCONFIG exported as chalklab create prints them.
chalklab sign images/node-kvm --out node-kvm-signed
chalkctl upgrade --image node-kvm-signed --allow-downtime

# Sign an image from a registry in the directory it is pulled into.
chalkctl image pull ghcr.io/example/node-kvm:v2 node-kvm-v2
chalklab sign node-kvm-v2
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
