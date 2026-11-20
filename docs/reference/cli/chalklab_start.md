---
title: "chalklab start"
description: "Start a stopped lab, or the VMs of a running lab that stopped"
---

# chalklab start

```sh
chalklab start [flags]
```

Starts a lab again from its state, with its disks, firmware variables, TPM state and ports. When
the supervisor runs, it starts the VMs that stopped (for example a VM whose node powered off).
When no supervisor runs (for example after a reboot of this machine), chalklab starts the
supervisor, which starts every VM. Each node boots its disk as it is, so start pulls no image,
even when the tag of an image from a registry has moved since create: a node runs the image
create made its disk from, or the one chalkctl upgrade installed since. The command needs no
secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--name`{ #flag-name } | string | the only lab | Lab to start. |

## Examples

```sh
chalklab start
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
