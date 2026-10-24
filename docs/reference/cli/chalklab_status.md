---
title: "chalklab status"
description: "Show the lab's VMs, ports and files"
---

# chalklab status

```sh
chalklab status [flags]
```

Shows the lab's supervisor, each VM with its node's type, whether it runs, the forwarded ports
of its chalkd and API server and its console log, and the paths of the lab's kubeconfig,
inventory, client file and Secure Boot db key and certificate. The command needs no secrets file
or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--name`{ #flag-name } | string | the only lab | Lab to show. |

## Examples

```sh
chalklab status
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
