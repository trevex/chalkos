---
title: "chalklab status"
description: "Show the lab's VMs, ports and files"
---

# chalklab status

Show the lab's VMs, ports and files

## Synopsis

Shows the lab's supervisor, each VM with its role, whether it runs, the forwarded ports of its
chalkd and API server and its console log, and the paths of the lab's kubeconfig, client file
and Secure Boot db key and certificate. The command needs no secrets file or client file.

```
chalklab status [flags]
```

## Examples

```
  chalklab status
```

## Options

```
      --cluster string   cluster whose lab to show (default the only lab)
  -h, --help             help for status
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

