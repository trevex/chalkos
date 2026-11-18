---
title: "chalkctl apply"
description: "Deliver a node's config from the inventory: short for config apply"
---

# chalkctl apply

```sh
chalkctl apply <node> [<node>...] [flags]
```

chalkctl apply is the short form of chalkctl config apply: it takes the same nodes and flags and
does the same.

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. chalkctl warns of each image
reference in the config that names a tag but no digest, and delivers the config all the same. See
[Change rules decide which changes a node accepts](../../how-it-works/node/config.md#change-rules-decide-which-changes-a-node-accepts).

With several nodes, config apply delivers to them one after the other and stops at the first that
fails; a node named twice gets the config once. --address gives each node's address as NODE=ADDR
where it is not the inventory's. chalkctl checks every node's status and config before the first
node gets anything and leaves a node that runs the config already alone, so the same command run
again continues a run that stopped. --staged, --diff and --no-confirm take one node.

A config that changes a version a layer bounds across the cluster, such as the Kubernetes API
server's, runs ordered: the layer reads the versions every node runs and refuses a config that
breaks its bounds, as Kubernetes's version skew policy does. The nodes that act for the others,
such as Kubernetes control planes, go first, one at a time, each held by its layers and on trial,
for --try or else 10 minutes, until its layers find it serving with the config; then the other
nodes. The first failure rolls that node's trial back at once, and chalkctl prints which nodes run
which version. --until deliver leaves the first node's trial open for config confirm. An ordered
run goes without the layers' payload flags. See
[Upgrade Kubernetes](../../kubernetes/upgrade-kubernetes.md).

--diff prints what the config changes, document by document, and checks nothing on the node.
--dry-run has the node check the config as it checks a delivery and changes nothing. It prints the
diff, the config the node would run, the outputs that would change, the effects with what fires
each, the storage changes, and the staged or pending config the delivery writes; a refusal exits 1.
--dry-run takes several nodes, and its error names the nodes whose config is refused apart from
those it could not check, such as a node on trial. It goes without --diff, --try, --no-confirm,
--ignore-unit and the layers' payload flags. With --staged the plan has no outputs or
effects, which that image takes when it boots.

For config delivered to the running image, --force makes failed runtime readiness checks
warnings and lets their effects continue. It still runs the probes, validates the config,
requires prerequisite actions to succeed, and uses the usual trial health checks. A dry run
shows the override without running those probes.

Each image on a node keeps its own config, which it checks itself. A delivery to the running image
goes to the staged image too, which checks it before anything changes; once the running image
applied it, the node writes the staged image's config, except that of a rollback's image while its
boot is not found healthy. When that write fails, the command fails naming it, and running it
again writes it. --staged delivers to the staged image alone, and nothing restarts. Without a
staged image, --staged holds the config pending for the next upgrade, which takes it unless it
carries config of its own. chalkctl checks it against the node's image in the inventory; the node
only parses it, as no image is there to check it against. A pending config carries no fallback
secret. A delivery to the running image replaces a pending config too, and config discard drops
it. See
[A node keeps one config slot per image](../../how-it-works/node/config.md#a-node-keeps-one-config-slot-per-image).

--try applies the config on trial for the duration given, from 10s to 24h: unless chalkctl
confirms it in time, the node rolls it back by itself. chalkctl confirms over a new connection,
once the node is healthy and its layers find it serving with the config; --ignore-unit leaves a
unit out of the health check, one unit per flag, and chalkctl warns of units that failed before the
trial. When the config is not confirmed, or the answer to its delivery is lost, chalkctl waits up to
two minutes after the deadline for the node's rollback, reports it and exits non-zero.
Interrupting chalkctl leaves the trial open. A trial delivers config alone and is refused for a
config that changes the node's storage; while it is open, the node refuses other deliveries,
upgrades and image rollbacks. --no-confirm leaves the trial open for config confirm, and config
rollback ends it at once. See
[A trial rolls a config back unless it is confirmed](../../how-it-works/node/config.md#a-trial-rolls-a-config-back-unless-it-is-confirmed).

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; with this flag, --force delivers it without asking and keeps
runtime readiness checks enforced. The command needs the secrets
file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of the node's chalkd, host or host:port, or NODE=ADDR of any node of the cluster; repeat it for several. |
| `--allow-downgrade`{ #flag-allow-downgrade } | boolean | `false` | Deliver an API server one minor release older than the newest the control planes run. |
| `--allow-downtime`{ #flag-allow-downtime } | boolean | `false` | Deliver a new API server version to a control plane of a cluster with one or two, whose API may be unavailable while that control plane's API server restarts. |
| `--diff`{ #flag-diff } | boolean | `false` | Print the diff against the node's config and change nothing; the node checks nothing. |
| `--dry-run`{ #flag-dry-run } | boolean | `false` | Have the node check the config as for a delivery and print the diff and plan; changes nothing. |
| `--force`{ #flag-force } | boolean | `false` | Apply config even if a runtime readiness check fails; with --kubernetes-share, deliver without asking whether the node is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--ignore-unit`{ #flag-ignore-unit } | list of string | — | With --try, leave the unit out of the health check the confirmation waits for, as the image's check leaves out the units it ignores. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--kubernetes-share`{ #flag-kubernetes-share } | boolean | `false` | Also deliver a new Kubernetes share, such as a new kubelet certificate for a worker. |
| `--no-confirm`{ #flag-no-confirm } | boolean | `false` | With --try, leave the trial open for config confirm. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--staged`{ #flag-staged } | boolean | `false` | Deliver the config to the image staged in the inactive slot, which checks it after chalkctl checks it against the node's image in the inventory; without one, the node holds it pending for the next upgrade; with --dry-run, the staged image checks it and nothing is kept; without one, --dry-run says what the node would hold pending. |
| `--try`{ #flag-try } | duration | `0s` | Apply the config on trial for the duration, from 10s to 24h: the node rolls it back by itself unless chalkctl confirms it over a new connection once the node is healthy. |
| `--until`{ #flag-until } | step | `confirm` | How far the delivery goes: deliver delivers to the first node that does not run the config, on trial, and leaves the trial open for config confirm; confirm delivers to every node. |

## Examples

```sh
# Apply w1's changed network and labels.
chalkctl apply w1 --secrets secrets.age

# Give w1 a new kubelet certificate.
chalkctl apply w1 --secrets secrets.age --kubernetes-share

# Show what w1's config in the inventory changes on the node, changing nothing.
chalkctl apply w1 --secrets secrets.age --diff

# Have w1 and w2 check their config, renderers included, and show what a delivery would do.
chalkctl apply w1 w2 --secrets secrets.age --dry-run

# Deliver w1's config for the image an upgrade staged, or without one hold it pending for w1's
# next upgrade.
chalkctl apply w1 --secrets secrets.age --staged

# Drop the config w1 holds pending.
chalkctl config discard w1 --secrets secrets.age

# Undo w1's last config delivery.
chalkctl config rollback w1 --secrets secrets.age

# Try w1's changed network for five minutes; w1 rolls it back unless chalkctl reaches it again.
chalkctl apply w1 --secrets secrets.age --try 5m

# Keep a trial open while you test by hand, then keep the config.
chalkctl apply w1 --secrets secrets.age --try 30m --no-confirm
chalkctl config confirm w1 --secrets secrets.age

# Deliver the cluster's config to every node. When it names a new API server, the control
# planes get it first, one at a time, each on trial until its static pods run the new images.
chalkctl apply cp1 cp2 cp3 w1 w2 --secrets secrets.age

# Walk the control planes by hand: deliver to the first that lacks the config, leave its trial
# open, check it, confirm it, and run the first command again for the next.
chalkctl apply cp1 cp2 cp3 --secrets secrets.age --until deliver
chalkctl config confirm cp1 --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
