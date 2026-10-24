---
title: "chalkctl rotate"
description: "Rotate os-ca, kubernetes-ca, service-account-key or encryption-key"
---

# chalkctl rotate

```sh
chalkctl rotate <kind> [flags]
```

Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept (every node trusts the new value besides the old one),
switch (the new value issues or signs), refresh (what the old value issued is issued again) and,
with --finish, finish (the old value is removed, and whatever it issued is refused from then on).
The secrets file records the phase reached and is updated in place, keeping its previous version
as &lt;file>.prev, unless --out names a new file; an encrypted file is encrypted again to the
recipients it records inside, which --recipient replaces. A rotation that stopped (at an
unreachable node, for example) or paused for the operator continues with --resume. One rotation
runs at a time, and one chalkctl command at a time changes the secrets file, holding &lt;file>.lock.
Nodes revoked with chalkctl node revoke are skipped and named: before each phase, rotate reads
them from the first control plane that says which nodes are revoked, and it stops while none
does. A cluster none of whose control planes was bootstrapped yet has revoked no node.
--ignore-revoked rotates without the list, as while the API server is down or the control planes
run a chalkd from before node revocation: every node of the inventory takes part, so remove
revoked nodes from the inventory first, or they receive the new value too. A node unrevoked
between two runs of a rotation takes part from the next run on, but it missed the phases it was
skipped in, and the rotation may stop at it; unrevoke nodes before or after a rotation, not while
one is paused. The command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | map of string | each node's address in the inventory | Address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated. |
| `--finish`{ #flag-finish } | boolean | `false` | Remove the old value from every node and the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | With --finish, finish the service-account key's rotation within the hour after its switch. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--ignore-revoked`{ #flag-ignore-revoked } | boolean | `false` | Rotate every node of the inventory, revoked ones included, without reading the revoked nodes, as while no control plane can say which are. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--out`{ #flag-out } | string | — | New file to write the changed secrets file to, instead of updating the secrets file in place. |
| `--public-out`{ #flag-public-out } | string | — | New file to write the public half to, instead of updating secrets.pub.json beside the secrets file in place. |
| `--recipient`{ #flag-recipient } | list of string | — | Age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out. |
| `--resume`{ #flag-resume } | boolean | `false` | Continue the rotation the secrets file records. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--timeout`{ #flag-timeout } | duration | `10m0s` | How long to wait for each node to apply a phase. |

## Examples

```sh
# Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
# trust the new CA before it issues certificates. It pauses again after the refresh.
chalkctl rotate kubernetes-ca --secrets secrets.age
chalkctl rotate kubernetes-ca --secrets secrets.age --resume
chalkctl rotate kubernetes-ca --secrets secrets.age --finish
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
