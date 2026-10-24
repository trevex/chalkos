---
title: "chalkctl upgrade"
description: "Install new images on the cluster's nodes, one control plane at a time"
---

# chalkctl upgrade

```sh
chalkctl upgrade [flags]
```

Installs a new image on each node: control planes one at a time, each only while etcd keeps its
quorum without it, then workers and nodes without Kubernetes in batches of --max-unavailable.
Without --image, each node, or each node --nodes names, gets the image the inventory names for it;
with --image, the nodes that run the image's variant get it, or those --nodes names. A node gets
the image in its inactive slot, is cordoned and drained, reboots into the image, and is uncordoned
once its boot was found healthy and it is Ready. A node that falls back to the image before stops
the run.

Each node gets its config with the image, checked against it before any node is drained, and
chalkctl prints a line of what it changes; --diff prints the whole diff and changes nothing.
Delivering config needs the admin role: with an operator client file, each node carries its own
config over. A new volume is enrolled with the recovery key, which only the secrets file gives, or
the password --password-file holds.

Run again, the command continues where it stopped, and --retry-failed installs the image once more
on nodes that fell back from it. See
[Upgrades](../../how-it-works/node/upgrades.md) and
[Kubernetes upgrades](../../how-it-works/kubernetes/upgrades.md). The command needs an
operator client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | map of string | each node's address in the inventory | Address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated. |
| `--allow-downtime`{ #flag-allow-downtime } | boolean | `false` | Upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--delete-emptydir-data`{ #flag-delete-emptydir-data } | boolean | `false` | Evict pods with emptyDir volumes too, deleting their data. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what each node's config changes against the config of the image it runs, document by document and field by field, and change nothing; needs the admin role. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--image`{ #flag-image } | string | the directory of each node's image in the inventory | Image to install on the nodes that run its variant, or on the nodes --nodes names whatever they run: a raw image with repart-output.json next to it, or an image directory. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--max-unavailable`{ #flag-max-unavailable } | integer | `1` | How many workers and nodes without Kubernetes upgrade at once. |
| `--no-reboot`{ #flag-no-reboot } | boolean | `false` | Install the images on the nodes without draining or rebooting them; they boot with their next reboot. |
| `--nodes`{ #flag-nodes } | string | every node of the cluster, or with --image every node that runs the image's variant | Comma-separated nodes to upgrade. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--retry-failed`{ #flag-retry-failed } | boolean | `false` | Install an image again, once, on nodes that fell back from it. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--timeout`{ #flag-timeout } | duration | `30m0s` | How long to wait for each node to come back healthy and for its drain. |

## Examples

```sh
# Upgrade the whole cluster, each node to the image the inventory names for it.
chalkctl upgrade

# Upgrade the nodes that run an image's variant, two at a time, with an image chalkbuild signed.
chalkctl upgrade --image ./worker-image --max-unavailable 2
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
