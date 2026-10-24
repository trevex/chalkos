---
title: "chalklab console"
description: "Follow a node's serial console"
---

# chalklab console

```sh
chalklab console <node> [flags]
```

Prints a node's serial console log and follows what the node writes until interrupted;
--follow=false prints the log and exits. The console is read-only. The command needs no
secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--follow`{ #flag-follow }, `-f` | boolean | `true` | Keep printing what the node writes. |
| `--name`{ #flag-name } | string | the only lab | Lab of the node. |

## Examples

```sh
chalklab console cp1

# Print the log so far.
chalklab console w1 -f=false
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
