---
title: "chalklab start"
description: "Start a stopped lab, or the VMs of a running lab that stopped"
---

# chalklab start

Start a stopped lab, or the VMs of a running lab that stopped

## Synopsis

Starts a lab again from its state, with its disks, firmware variables, TPM state and ports. When
the supervisor runs, it starts the VMs that stopped, as one the node powered off; otherwise, as
after a reboot of this machine, chalklab starts the supervisor, which starts every VM.

```
chalklab start [flags]
```

## Examples

```
  chalklab start
```

## Options

```
      --cluster string   cluster whose lab to start (default the only lab)
  -h, --help             help for start
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

