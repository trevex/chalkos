---
title: "chalkctl upgrade"
description: "Install new images on the cluster's nodes, one control plane at a time"
---

# chalkctl upgrade

```sh
chalkctl upgrade [flags]
```

Installs on each node a new image: control planes one at a time, each only while etcd keeps its
quorum without it, then workers and nodes without Kubernetes in batches of --max-unavailable.
Without --image each node (every node of the inventory, or those --nodes names) gets the image the
inventory names for it, which must be the build whose root hash its directory's metadata.json
names, as status compares nodes with it: rebuild the image or link its directory again otherwise.
Each node pulls an image the inventory names in a registry itself, by the digest chalkctl resolved
its reference to, through the node's Mirror of the registry's host and with its Registry
documents. A node that runs on another platform than its image stops the run before any node is
sent anything. With --image the nodes that run the image's variant get it, or
those --nodes names whatever they run, and nodes on another platform are skipped and named.
A node may get an image of another variant than the one it runs, which chalkctl prints.

Each node gets its config with the image: the documents of the files the inventory gives it,
checked against the image before any node is drained. The node checks them against the image
again and keeps them for it, so a fallback boots the image before with its own config. Storage
changes count against the node's storage as with apply-config: a destructive one is refused, a new
volume is created before the image boots, enrolled with the recovery key, which only the secrets
file gives, or the password --password-file holds. chalkctl asks for each node's password before
it drains the first node, one node after the other. Without an inventory a node carries the config
of the image it runs over, checked against the new image. A node that has the image staged already
gets it again only when the config kept for it is not the inventory's. Before it sends a node the
image, chalkctl prints a line of what the node's config changes against the config of the image
it runs, as apply-config does; --diff prints the whole diff for every node and changes nothing.

Delivering config with the upgrade needs the admin role, which the secrets file and an admin
client file grant. With an operator client file chalkctl sends no config, says so once when there
is an inventory, and each node carries its own over; --diff needs the admin role.

A node gets the image in its inactive slot, is cordoned and drained within its pods'
PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found healthy
and the node is Ready. A node whose image never becomes healthy falls back to the image before,
and the run stops there, showing what that boot logged; with --retry-failed a node that fell
back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. Without an inventory, --address NODE=ADDR
names the nodes, and --image is needed. The command needs an operator client file or the secrets
file.

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
