---
title: "chalkctl rotate"
description: "Rotate os-ca, node-ca, kubernetes-ca, service-account-key or encryption-key"
---

# chalkctl rotate

```sh
chalkctl rotate <kind> [flags]
```

Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept, switch, refresh and, with --finish, finish. A rotation
that stopped, at an unreachable node for example, or paused for the operator continues with
--resume. See
[A rotation replaces a CA in four phases](../../how-it-works/node/certificates.md#a-rotation-replaces-a-ca-in-four-phases).

The secrets file records the phase reached and is updated in place, keeping its previous version
as &lt;file>.prev, unless --out names a new file. An encrypted file is encrypted again to the
recipients it records, which --recipient replaces. One rotation runs at a time, and one chalkctl
command at a time changes the secrets file, holding &lt;file>.lock.

Nodes revoked with chalkctl node revoke are skipped and named; rotate stops while a layer cannot
say which nodes are revoked, as while no Kubernetes control plane answers, unless none was
bootstrapped yet. --ignore-revoked rotates without the lists, and every node of the inventory
takes part, so remove revoked nodes from the inventory first. The command needs the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | map of string | each node's address in the inventory | Address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated. |
| `--finish`{ #flag-finish } | boolean | `false` | Remove the old value from every node and the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | With --finish, finish although the kind asks to wait, as the service-account key does within the hour after its switch. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--ignore-revoked`{ #flag-ignore-revoked } | boolean | `false` | Rotate every node of the inventory, revoked ones included, without reading the revoked nodes, as while no layer can say which are. |
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

# Rotate the node CA. It runs in one go: every node gets a certificate of the new one.
chalkctl rotate node-ca --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
