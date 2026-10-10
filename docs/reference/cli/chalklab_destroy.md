---
title: "chalklab destroy"
description: "Stop the lab and remove its state"
---

# chalklab destroy

Stop the lab and remove its state

## Synopsis

Stops the lab's supervisor, VMs, TPMs and network switch and removes its state: disks,
firmware variables, TPM state, keys, console logs, kubeconfig and client file. The state is kept
when anything of the lab is still running. The command needs no secrets file or client file.

```
chalklab destroy [flags]
```

## Examples

```
  chalklab destroy --cluster lab
```

## Options

```
      --cluster string   cluster whose lab to remove (default the only lab)
  -h, --help             help for destroy
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

