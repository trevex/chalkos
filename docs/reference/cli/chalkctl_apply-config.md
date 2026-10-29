---
title: "chalkctl apply-config"
description: "Deliver a node's config from the inventory"
---

# chalkctl apply-config

```sh
chalkctl apply-config <node> [flags]
```

Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. See
[Change rules decide which changes a node accepts](../../how-it-works/node/config.md#change-rules-decide-which-changes-a-node-accepts).

Each image on a node keeps its own config. --staged delivers the config for the image staged to
boot next, as an upgrade with --no-reboot leaves it, and nothing restarts. Without --staged,
chalkctl warns of a staged image, whose config stays as it is. A config for the running image that
adds storage the staged image's config lacks is refused: deliver the config with --staged first.

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
image ignores and those --ignore-unit names, one unit per flag. chalkctl warns of units that failed
before the trial, which keep the check from passing unless they recover. When the config is not
confirmed, or the answer to its delivery is lost, as when the config cuts chalkctl off, chalkctl
waits up to two minutes after the deadline for the node's rollback, reports it and exits non-zero.
Interrupting chalkctl leaves the trial open. A trial delivers config alone and is refused for a
config that changes the node's storage. While it is open, the node refuses other deliveries,
upgrades and image rollbacks; --rollback rolls the config back at once and ends the trial, and a
reboot boots the config from before it. --no-confirm leaves the trial open, and --confirm confirms
it later, trying until the trial's deadline while the node is unhealthy; --confirm needs no
inventory. A delivery on trial that fails after it changed the config leaves the trial open, and
chalkctl does not confirm it. When an attempt to roll the config back fails, the node tries again
each minute and --confirm still keeps the config, unless the failed attempt changed the node;
--rollback then finishes the rollback. After a trial's rollback, the config on trial is the node's
previous config, which --rollback delivers again without a trial.

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; --force delivers it without asking. The command needs the secrets
file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--confirm`{ #flag-confirm } | boolean | `false` | Confirm the node's open trial once the node is healthy, so it keeps the config on trial. |
| `--diff`{ #flag-diff } | boolean | `false` | Print what the config changes in the node's config, document by document and field by field, and change nothing. |
| `--force`{ #flag-force } | boolean | `false` | With --kubernetes-share, deliver without asking whether the node is revoked. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--ignore-unit`{ #flag-ignore-unit } | list of string | — | With --try, leave the unit out of the health check the confirmation waits for, as the image's check leaves out the units it ignores. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--kubernetes-share`{ #flag-kubernetes-share } | boolean | `false` | Also deliver a new Kubernetes share, such as a new kubelet certificate for a worker. |
| `--no-confirm`{ #flag-no-confirm } | boolean | `false` | With --try, leave the trial open for apply-config --confirm. |
| `--password-file`{ #flag-password-file } | string | — | File holding the password of a node whose fallback is a password. |
| `--rollback`{ #flag-rollback } | boolean | `false` | Deliver the node's previous config again, the one its image ran before the last delivery, instead of the inventory's. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--staged`{ #flag-staged } | boolean | `false` | Deliver the config for the image staged to boot next instead, checked against the node's image in the inventory. |
| `--try`{ #flag-try } | duration | `0s` | Apply the config on trial for the duration, from 10s to 24h: the node rolls it back by itself unless chalkctl confirms it over a new connection once the node is healthy. |

## Examples

```sh
# Apply w1's changed network and labels.
chalkctl apply-config w1 --secrets secrets.age

# Give w1 a new kubelet certificate.
chalkctl apply-config w1 --secrets secrets.age --kubernetes-share

# Show what w1's config in the inventory changes on the node, changing nothing.
chalkctl apply-config w1 --secrets secrets.age --diff

# Deliver w1's config for the image an upgrade with --no-reboot staged.
chalkctl apply-config w1 --secrets secrets.age --staged

# Undo w1's last config delivery.
chalkctl apply-config w1 --secrets secrets.age --rollback

# Try w1's changed network for five minutes; w1 rolls it back unless chalkctl reaches it again.
chalkctl apply-config w1 --secrets secrets.age --try 5m

# Keep a trial open while you test by hand, then keep the config.
chalkctl apply-config w1 --secrets secrets.age --try 30m --no-confirm
chalkctl apply-config w1 --secrets secrets.age --confirm
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
