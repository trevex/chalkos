---
title: "chalkctl apply-config"
description: "Deliver a node's config from the inventory"
---

# chalkctl apply-config

```sh
chalkctl apply-config <node> [<node>...] [flags]
```

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. chalkctl warns of each image
reference in the config that names a tag but no digest, and delivers the config all the same. See
[Change rules decide which changes a node accepts](../../how-it-works/node/config.md#change-rules-decide-which-changes-a-node-accepts).

With several nodes, apply-config delivers to them one after the other and stops at the first that
fails; a node named twice gets the config once. --address gives each node's address as NODE=ADDR
where it is not the inventory's. chalkctl reads each node's status and checks its config against
its image before the first node gets anything, and leaves a node that runs the config already
alone, so the same command run again continues a run that stopped. --discard, --confirm,
--rollback, --staged, --diff and --no-confirm take one node.

When the config changes a version that a layer of the nodes' images bounds across the cluster,
such as the Kubernetes API server's, against the config a node runs, the run is ordered, with one
node or several. A run that changes no such version asks no layer anything. An ordered run reads
the versions every node of the cluster runs, and the layer refuses a config that would break its
bounds, as Kubernetes's version skew policy does. The nodes that act for the others, such as
Kubernetes control planes, go first, one at a time. Its layers hold each first, as Kubernetes
waits for etcd to keep its quorum; then it gets the config on trial, for --try or else 10 minutes,
until its layers find it serving with the config, as Kubernetes finds a control plane's static
pods running the new images. Then the other nodes get the config as in any run. The first failure
rolls that node's trial back at once, and chalkctl prints which nodes run which version. --until
deliver delivers on trial to the first node that does not run the config and leaves its trial open
for --confirm. An ordered run goes without the layers' payload flags. See
[Upgrade Kubernetes](../../kubernetes/upgrade-kubernetes.md).

--diff prints what the config changes in the node's config, document by document, and checks
nothing on the node. --dry-run has the node check the config as it checks a delivery, with its
facts, the image's renderers and their checks, and changes nothing. It prints the diff, then the
config the node would run, the outputs that would change, the effects the delivery would take with
what fires each, and the storage changes. A refusal is the delivery's, and the command exits 1.
--dry-run takes several nodes and checks them one after the other; its error names the nodes whose
config is refused apart from those it could not check, such as a node on trial. It goes without
--diff, --rollback, --confirm, --try, --no-confirm, --ignore-unit, --force and the layers'
payload flags. An effect whose rule compares the node's facts before and after the delivery is
planned with the facts as they are. The plan ends with the staged image's config or the pending
config, when the delivery writes either. With --staged, the staged image checks the config with
its own chalkd and renderers, and the plan has no outputs and no effects: that image renders its
outputs and takes its effects when it boots.

Each image on a node keeps its own config. The image in the node's inactive slot, staged by
upgrade --until stage or active after upgrade --until activate, boots with the config it checked
itself. A delivery to the running image goes to that image too: it checks the config with its own
chalkd and renderers, against the storage the running image's change leaves, before anything
changes, and its refusal refuses the delivery. Once the running image applied the config, the node
writes the staged image's config and says so. When that write fails, the staged image keeps its
earlier config, the command fails naming it, and running it again writes it. The image a rollback
staged while the running image's boot is not found healthy keeps the config it last ran, and the
node warns of it. --staged delivers the config to the staged image alone, and nothing restarts.

On a node without a staged image, --staged holds the config pending for the next upgrade, in place
of any config held before. chalkctl checks it against the node's image in the inventory; the node
only parses it, as no image is there to check it against. The next upgrade that carries no config
of its own, such as an operator's, gives the image the pending config in place of the running one,
and the image checks it before the node installs anything. An upgrade that carries config replaces
it. Either way the node drops the pending config once the image is staged; an upgrade
that fails keeps it, and an image's refusal names it. A pending config is kept without the fallback
secret, so an upgrade with one that adds an encrypted volume needs the config from the inventory.
A delivery to the running image replaces a pending config too, once the running image applied it,
so the next upgrade boots the config delivered last. --staged --dry-run says the config would be
held pending and keeps nothing. --staged --discard drops the pending config and needs no inventory.

--rollback delivers the node's previous config again instead of the inventory's: the config the
running image ran before the last delivery, which the node keeps. It goes through the same checks,
storage changes and restarts as any delivery, so the image's rules may refuse it, as when the
config it replaces added a volume. The config it replaces becomes the previous one, so a second
--rollback undoes the first. It restores the config documents alone: the OS CAs, the node
certificate and layer payloads such as a Kubernetes share stay as they are. An upgrade's image
starts without a previous config. With --diff it prints what the rollback changes. It needs no
inventory.

--try applies the config on trial for the duration given, from 10s to 24h. Unless chalkctl
confirms the config within it, the node rolls the config back by itself through the same steps as
--rollback. chalkctl confirms it over a new connection, which proves that the config left the node
reachable, once the node is healthy as an upgrade's boot must be: chalkd serves, no unit of the
boot failed, and the image's layers find their services healthy. The check leaves out the units the
image ignores and those --ignore-unit names, one unit per flag. Before it confirms, chalkctl also
waits for the image's layers to find the node serving with the config, as Kubernetes finds its Node
Ready and a control plane's static pods on the images its config names; when they do not by the
deadline, chalkctl rolls the config back at once. chalkctl warns of units that failed before the
trial, which keep the check from passing unless they recover. When the config is not
confirmed, or the answer to its delivery is lost, as when the config cuts chalkctl off, chalkctl
waits up to two minutes after the deadline for the node's rollback, reports it and exits non-zero.
Interrupting chalkctl leaves the trial open. A trial delivers config alone and is refused for a
config that changes the node's storage. While it is open, the node refuses other deliveries,
upgrades and image rollbacks; --rollback rolls the config back at once and ends the trial, and a
reboot boots the config from before it. --no-confirm leaves the trial open, and --confirm confirms
it later, trying until the trial's deadline while the node is unhealthy, or for two minutes past
it, after a failed rollback; --confirm needs no inventory. A delivery on trial that fails after it
changed the config leaves the trial open, and chalkctl does not confirm it. When an attempt to roll
the config back fails, the node tries again each minute and --confirm still keeps the config,
unless the failed attempt changed the node; --rollback then finishes the rollback. After a trial's
rollback, the config on trial is the node's previous config, which --rollback delivers again
without a trial. The staged image checks config on trial as any delivery, but its config, or the
pending config, is written only once the trial is confirmed, so a rollback leaves it as it was.

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; --force delivers it without asking. The command needs the secrets
file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of the node's chalkd, host or host:port, or NODE=ADDR of any node of the cluster; repeat it for several. |
| `--allow-downgrade`{ #flag-allow-downgrade } | boolean | `false` | Deliver an API server one minor release older than the newest the control planes run. |
| `--allow-downtime`{ #flag-allow-downtime } | boolean | `false` | Deliver a new API server version to a control plane of a cluster with one or two, whose API may be unavailable while that control plane's API server restarts. |
| `--confirm`{ #flag-confirm } | boolean | `false` | Confirm the node's open trial once the node is healthy, so it keeps the config on trial. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what the config changes in the node's config, document by document and field by field, and change nothing; the node checks nothing, which --dry-run has it do. |
| `--discard`{ #flag-discard } | boolean | `false` | With --staged, drop the config the node holds pending for its next upgrade; needs no inventory. |
| `--dry-run`{ #flag-dry-run } | boolean | `false` | Have the node check the config as it checks a delivery, its renderers and facts included, print the diff and what the delivery would change, and change nothing; with --staged, the staged image checks it; takes several nodes. |
| `--force`{ #flag-force } | boolean | `false` | With --kubernetes-share, deliver without asking whether the node is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--ignore-unit`{ #flag-ignore-unit } | list of string | — | With --try, leave the unit out of the health check the confirmation waits for, as the image's check leaves out the units it ignores. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--kubernetes-share`{ #flag-kubernetes-share } | boolean | `false` | Also deliver a new Kubernetes share, such as a new kubelet certificate for a worker. |
| `--no-confirm`{ #flag-no-confirm } | boolean | `false` | With --try, leave the trial open for apply-config --confirm. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--rollback`{ #flag-rollback } | boolean | `false` | Deliver the node's previous config again, the one its image ran before the last delivery, instead of the inventory's. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--staged`{ #flag-staged } | boolean | `false` | Deliver the config for the image staged in the inactive slot instead, active or not, which checks it with its own chalkd after chalkctl checks it against the node's image in the inventory; without a staged image, the node holds it pending, unchecked, for the next upgrade, which checks it against its image; with --dry-run, the staged image checks it and nothing is kept; without one, --dry-run says what the node would hold pending. |
| `--try`{ #flag-try } | duration | `0s` | Apply the config on trial for the duration, from 10s to 24h: the node rolls it back by itself unless chalkctl confirms it over a new connection once the node is healthy. |
| `--until`{ #flag-until } | step | `confirm` | How far the delivery goes: deliver delivers to the first node that does not run the config, on trial, and leaves the trial open for apply-config --confirm; confirm delivers to every node. |

## Examples

```sh
# Apply w1's changed network and labels.
chalkctl apply-config w1 --secrets secrets.age

# Give w1 a new kubelet certificate.
chalkctl apply-config w1 --secrets secrets.age --kubernetes-share

# Show what w1's config in the inventory changes on the node, changing nothing.
chalkctl apply-config w1 --secrets secrets.age --diff

# Have w1 and w2 check their config, renderers included, and show what a delivery would do.
chalkctl apply-config w1 w2 --secrets secrets.age --dry-run

# Deliver w1's config for the image an upgrade staged, or without one hold it pending for w1's
# next upgrade.
chalkctl apply-config w1 --secrets secrets.age --staged

# Drop the config w1 holds pending.
chalkctl apply-config w1 --secrets secrets.age --staged --discard

# Undo w1's last config delivery.
chalkctl apply-config w1 --secrets secrets.age --rollback

# Try w1's changed network for five minutes; w1 rolls it back unless chalkctl reaches it again.
chalkctl apply-config w1 --secrets secrets.age --try 5m

# Keep a trial open while you test by hand, then keep the config.
chalkctl apply-config w1 --secrets secrets.age --try 30m --no-confirm
chalkctl apply-config w1 --secrets secrets.age --confirm

# Deliver the cluster's config to every node. When it names a new API server, the control
# planes get it first, one at a time, each on trial until its static pods run the new images.
chalkctl apply-config cp1 cp2 cp3 w1 w2 --secrets secrets.age

# Walk the control planes by hand: deliver to the first that lacks the config, leave its trial
# open, check it, confirm it, and run the first command again for the next.
chalkctl apply-config cp1 cp2 cp3 --secrets secrets.age --until deliver
chalkctl apply-config cp1 --secrets secrets.age --confirm
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
