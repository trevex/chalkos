---
title: "chalklab sign"
description: "Sign an image with the lab's Secure Boot keys, for upgrades"
---

# chalklab sign

Sign an image with the lab's Secure Boot keys, for upgrades

## Synopsis

Signs the boot loader and UKIs of an image with the lab's Secure Boot db key, so the lab's VMs
boot it; an upgrade with an image built elsewhere needs that. An image in the Nix store cannot be
changed, so --out copies the image into a directory and signs the copy. The command needs no
secrets file or client file.

```
chalklab sign <image> [flags]
```

## Examples

```
  # Sign a copy of an image in the Nix store and upgrade the lab's nodes of its role with it.
  chalklab sign /nix/store/...-chalkos-worker-image --out worker-image
  chalkctl upgrade --image worker-image --config ~/.local/state/chalklab/lab/chalkctl.json
```

## Options

```
      --cluster string   cluster whose lab's keys to sign with (default the only lab)
  -h, --help             help for sign
      --out string       directory to copy the image to (made when missing) and sign there; needed for an image in the Nix store, which cannot be signed in place
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

