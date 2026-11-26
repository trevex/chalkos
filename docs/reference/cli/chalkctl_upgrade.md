---
title: "chalkctl upgrade"
description: "Install new images on the cluster's nodes, or boot their previous ones again"
---

# chalkctl upgrade

```sh
chalkctl upgrade [flags]
```

Installs a new image on each node: first the nodes that act for the others, such as Kubernetes
control planes, one at a time, then the other nodes in batches of --max-unavailable. Without
--image, each node, or each node --nodes names, gets the image the inventory names for it; with
--image, the nodes running an image of its name get it, or those --nodes names. Each node goes
through the steps the step-by-step path runs one at a time: the node stages the image in its
inactive slot once the image's own checker accepted its config, as upgrade --until stage stops
there; it activates the image, which then boots next, as chalkctl node activate does; and it
reboots into the image as chalkctl node reboot does. Its layers hold it before the reboot and
release it once its boot was found healthy: Kubernetes keeps etcd's quorum, drains the node and
uncordons it once it is Ready. A node that falls back to the image before stops the run. A node
not found healthy within --timeout stops it too and stays held; the command run again waits for it
and releases it, unless the node fell back meanwhile: like any node that fell back, it then stays
held until a run reboots it into a fixed image, or into the same image once more with
--retry-failed, finds that boot healthy and has its layers release it. Staging or activating an
image does not release it, nor does a run with --force, which leaves the layers out.
--until stage or --until activate stops each node after that step and holds no node.

Before any node gets its image, the image's layers check the versions it brings against what the
cluster runs and refuse an image that would break their bounds, as Kubernetes refuses a kubelet
newer than the oldest API server the control planes run. An upgrade also refuses a config that
changes a version the layers bound against the config a node runs, such as the Kubernetes API
server's: chalkctl config apply delivers that change first, in the order the layers need.

--force recovers the nodes explicitly named by --nodes when their cluster cannot serve an
ordinary upgrade. It skips cluster version checks, config rollout guards and layer coordination:
no layer role queries, quorum checks, draining or uncordoning. It needs only the selected nodes'
chalkd endpoints, processes them in --max-unavailable batches without coordinator ordering, and
still checks credentials, image integrity and config, and waits for the new boot to pass the
node's health checks. It may interrupt workloads or lose etcd quorum. The node's protection of a
fallback image while its running boot is unblessed still applies. The flag is client-side and
works with installed nodes that have no force option of their own.

Each node gets its config with the image, checked against it before any node is drained, and
chalkctl prints a line of what it changes and warns of each image reference in the config that
names a tag but no digest; --diff prints the whole diff and changes nothing.
Delivering config needs the admin role: with an operator client file, each node gives the image
its pending config, else carries its own config over. A new volume is enrolled with the recovery
key, which only the secrets file gives, or the password --password-file holds.

--rollback boots the image the nodes ran before their last upgrade again instead of installing
one; their inactive slots still hold it with its config. Every node named, or every node of the
cluster, must roll back to the same image or run it already, or the command refuses and asks for
--nodes. The run holds and releases each node as an upgrade does; it stages nothing, so it takes
--until activate and reboot alone. Run again, it continues with the same image, and an upgrade to
the inventory's image undoes it. See
[A rollback boots the image before again](../../how-it-works/node/upgrades.md#a-rollback-boots-the-image-before-again).

Run again, an upgrade continues where it stopped, and --retry-failed installs the image once more
on nodes that fell back from it. See
[Upgrades](../../how-it-works/node/upgrades.md) and
[Kubernetes upgrades](../../how-it-works/kubernetes/upgrades.md). The command needs an
operator client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd as NODE=ADDR, or as ADDR alone when the command names one node, where ADDR is host or host:port; may be repeated. |
| `--allow-downtime`{ #flag-allow-downtime } | boolean | `false` | Reboot a control plane of a cluster with one or two, whose etcd loses its quorum and API server is down while it reboots. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--delete-emptydir-data`{ #flag-delete-emptydir-data } | boolean | `false` | Evict pods with emptyDir volumes too, deleting their data. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what each node's config changes against the config of the image it runs, document by document and field by field, and change nothing; needs the admin role. |
| `--force`{ #flag-force } | boolean | `false` | Recover explicitly named nodes without cluster version checks, quorum checks or draining; still validate the image and config and wait for a healthy boot. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--image`{ #flag-image } | string | the directory of each node's image in the inventory | Image to install on the nodes running an image of its name, or on the nodes --nodes names whatever they run: a raw image with repart-output.json next to it, or an image directory. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--max-unavailable`{ #flag-max-unavailable } | integer | `1` | How many nodes upgrade at once after the nodes that act for the others, such as Kubernetes control planes, which upgrade one at a time. |
| `--nodes`{ #flag-nodes } | string | every node of the cluster, or with --image every node running an image of its name | Comma-separated nodes to upgrade. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--retry-failed`{ #flag-retry-failed } | boolean | `false` | Install an image again, once, on nodes that fell back from it. |
| `--rollback`{ #flag-rollback } | boolean | `false` | Boot the image the nodes ran before their last upgrade again, which their inactive slots still hold, instead of installing an image. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--timeout`{ #flag-timeout } | duration | `30m0s` | How long to wait for each node to come back healthy and for its drain. |
| `--until`{ #flag-until } | step | `reboot` | How far each node gets: stage installs its image and leaves it staged, which chalkctl node activate makes boot next, with the same --image when the run had one; activate makes it boot next, which chalkctl node reboot boots; reboot drains and reboots the node and waits until it is healthy. |

## Examples

```sh
# Upgrade the whole cluster, each node to the image the inventory names for it.
chalkctl upgrade

# Recover a node whose cluster cannot serve the upgrade.
chalkctl upgrade --nodes cp1 --force

# Upgrade the nodes running an image of its name, two at a time, with an image chalkbuild signed.
chalkctl upgrade --image ./worker-image --max-unavailable 2

# Upgrade w1 step by step: stage its image, make it boot next, reboot into it.
chalkctl upgrade --nodes w1 --until stage
chalkctl node activate w1
chalkctl node reboot w1

# Boot the image w1 and w2 ran before their last upgrade again.
chalkctl upgrade --rollback --nodes w1,w2
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
