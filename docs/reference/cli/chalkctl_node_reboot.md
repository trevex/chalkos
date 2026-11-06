---
title: "chalkctl node reboot"
description: "Reboot a node, held by its layers, and wait until it is healthy again"
---

# chalkctl node reboot

```sh
chalkctl node reboot <node> [flags]
```

Reboots a node as chalkctl upgrade does once the node's image is active: its layers hold it
first, as Kubernetes keeps etcd's quorum while a control plane reboots and drains the node; then
the node reboots, and chalkctl waits until it runs the image it boots next, an image activated by
chalkctl node activate or the one it ran, and its boot was found healthy, and its layers release
it, as Kubernetes uncordons it once its Node is Ready. The node checks the health of the new boot
itself, also of a boot of an entry blessed before, which the boot loader does not count. A node
that falls back from the image stops the command and stays held, and so does a node whose boot
settled and stays unhealthy for a minute: the boot settled once systemd finished starting it and
the image's health timeout, 300 seconds by default, passed since. A check that fails once, such as
etcd answering late, does not stop the command, and a healthy look starts the minute over. When
the node's status gave no ID of the boot before the reboot, chalkctl cannot tell the new boot from
that one and waits for --timeout instead. A boot not found healthy within --timeout stops the
command too. --no-drain reboots the node without its layers holding or releasing it, and still
waits. Draining a node goes through the cluster's control planes, which the inventory names.

A node in maintenance mode, reached with --fingerprint or --insecure, reboots without more. The
command needs an operator client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of string | each node's address in the inventory | Address of the node's chalkd, host or host:port, or NODE=ADDR of another node of the cluster, such as a control plane the drain goes through; repeat it for several. |
| `--allow-downtime`{ #flag-allow-downtime } | boolean | `false` | Reboot a control plane of a cluster with one or two, whose etcd loses its quorum and API server is down while it reboots. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--delete-emptydir-data`{ #flag-delete-emptydir-data } | boolean | `false` | Evict pods with emptyDir volumes too, deleting their data. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--no-drain`{ #flag-no-drain } | boolean | `false` | Reboot the node without its layers holding it before and releasing it after, such as without draining a Kubernetes node. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--timeout`{ #flag-timeout } | duration | `30m0s` | How long to wait for the node to come back healthy and for its drain. |

## Examples

```sh
# Drain w1, reboot it into the image it boots next and uncordon it once it is Ready.
chalkctl node reboot w1

# Reboot w1 without draining it first.
chalkctl node reboot w1 --no-drain
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's access, activate its staged image, reboot it and show its diagnostics and routes
