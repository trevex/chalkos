---
title: "chalklab console"
description: "Follow a node's serial console"
---

# chalklab console

Follow a node's serial console

## Synopsis

Prints a node's serial console log and follows what the node writes until interrupted;
--follow=false prints the log and exits. The console is read-only. The command needs no
secrets file or client file.

```
chalklab console <node> [flags]
```

## Examples

```
  chalklab console cp1

  # Print the log so far.
  chalklab console w1 -f=false
```

## Options

```
      --cluster string   cluster of the node's lab (default the only lab)
  -f, --follow           keep printing what the node writes (default true)
  -h, --help             help for console
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine

