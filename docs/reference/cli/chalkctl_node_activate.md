---
title: "chalkctl node activate"
description: "Make the image staged on a node boot next"
---

# chalkctl node activate

```sh
chalkctl node activate <node> [flags]
```

Makes the image staged on a node boot next, as chalkctl upgrade --until stage leaves it:
the node renames the image's UKI to its boot entry's name with the tries the image asks for, so
boot counting falls back to the running image should its boots fail. chalkctl names the image the
inventory gives the node, by its root hash, and the node refuses when it has no image staged or
another one. The node checks the UKI again, and not the config: the image's own checker passed
the config before the image was staged. Asked again, the command says that the image boots next
already. The image boots with the node's next reboot, which chalkctl node reboot runs. The command
needs an operator client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl upgrade --nodes w1 --until stage
chalkctl node activate w1
chalkctl node reboot w1
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's access, activate its staged image, reboot it and show its diagnostics and routes
