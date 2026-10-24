---
title: "chalklab destroy"
description: "Stop the lab and remove its state"
---

# chalklab destroy

```sh
chalklab destroy [flags]
```

Stops the lab's supervisor, VMs, TPMs and network switch and removes its state: disks,
firmware variables, TPM state, keys, console logs, kubeconfig, inventory and client file. The
state is kept when anything of the lab is still running. The command needs no secrets file or
client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--name`{ #flag-name } | string | the only lab | Lab to remove. |

## Examples

```sh
chalklab destroy --name lab
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
