---
title: "Rotate certificates and CAs"
description: "Issue client files, renew node certificates and rotate the cluster's CAs and keys"
---

# Rotate certificates and CAs

This guide gives people access to a cluster, renews what does not renew itself, replaces the
cluster's CAs and keys, and takes access away again. All of it starts from the
[secrets file](../reference/glossary.md#secrets-file), which holds every CA's key. A
[rotation](../reference/glossary.md#rotation) changes what every node trusts, one phase at a
time, while the cluster keeps running; it is also the only way to revoke a client file or a
kubeconfig.

## Before you begin

- The secrets file and the cluster's flake. Every command on this page needs the secrets file.
- For a rotation, every node of the [cluster definition](../reference/glossary.md#cluster-definition) running and reachable: each phase waits
  until each node confirms it, and a node that does not answer stops the phase until it does.
- [Certificates](../concepts/certificates.md) shows the hierarchy of CAs this guide changes.

## Give someone access to the nodes

A [client file](../reference/glossary.md#client-file) lets a person run chalkctl without the
secrets file. [`chalkctl config new`](../reference/cli/chalkctl_config_new.md) issues one for a
name and a [client role](../reference/glossary.md#client-role):

```console
$ chalkctl config new --name=alice --role=operator --ttl=2160h --out=alice.json
wrote alice.json for alice with the operator role; its certificate expires on 2027-01-08
```

A [reader](../reference/glossary.md#reader) may read node information, disks, status, logs and
etcd's members; an [operator](../reference/glossary.md#operator) may also reboot, drain, uncordon
and upgrade nodes; an [admin](../reference/glossary.md#admin) may call every method.
[Security model](../concepts/security.md#who-may-call-the-node-api) lists the methods of each
role. Without `--out` the file goes to `~/.config/chalkos/config`, where chalkctl looks
for it; `--config` or `$CHALKOSCONFIG` names another. chalkctl warns 30 days before the
certificate expires. Hand the file over as you would a private key: it holds one.

## Give someone access to the Kubernetes API

[`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) writes a kubeconfig whose client
certificate puts its holder in `chalkos:cluster-admins`, which is bound to `cluster-admin`:

```console
$ chalkctl kubeconfig --name=alice --ttl=2160h --out=alice.kubeconfig
wrote alice.kubeconfig; its certificate is valid for 2160h0m0s
```

`--name` becomes the Kubernetes user name, which authorisation and audit logs see. Every
kubeconfig chalkctl issues grants full access; narrower roles need an authenticator you add to
the API server.

## Renew a node certificate by hand

Nodes with Kubernetes renew their [node certificates](../reference/glossary.md#node-certificate)
through a [control plane](../reference/glossary.md#control-plane) once two thirds of the year's lifetime have passed. Two kinds of node
need [`chalkctl node renew`](../reference/cli/chalkctl_node_renew.md) instead: a node whose role
has no Kubernetes, which has no control plane to ask, and a node whose certificate expired, for
example after months switched off.

```console
$ chalkctl node renew w1
w1 serves a new node certificate; it expires 2027-10-10T09:12:44Z
```

For an expired node, chalkctl trusts the node's old key, which
[Certificates](../concepts/certificates.md#what-if-a-certificate-expired) weighs. Renewing before
expiry avoids that; the status line `node  expires ...  less than a third of its lifetime remains`
is the reminder.

## Rotate the node CA

The [node CA](../reference/glossary.md#node-ca) issues every node certificate, and control
planes hold it to renew them. It is valid for five years, and control planes report
`node CA expires <date>; run chalkctl node-ca rotate` once less than 18 months remain, because
no node certificate may outlive it.
[`chalkctl node-ca rotate`](../reference/cli/chalkctl_node-ca_rotate.md) issues a new one from
the [OS CA](../reference/glossary.md#os-ca) and delivers it to every control plane:

```console
$ chalkctl node-ca rotate
...
secrets.age holds a new node CA, which expires 2031-10-09T09:20:03Z; the previous version is secrets.age.prev; secrets.pub.json holds the new public half
cp1 renews node certificates with the new node CA
cp2 renews node certificates with the new node CA
cp3 renews node certificates with the new node CA
```

Certificates of the old node CA stay valid until they expire, because they chain to the same OS
CA; nodes pick up certificates of the new one at their next renewal. A control plane that was
unreachable gets the new node CA later with
`chalkctl apply-identity <node> --kubernetes-share`, as the command's error says.

## Choose what to rotate

[`chalkctl rotate`](../reference/cli/chalkctl_rotate.md) replaces one of four kinds of value:

| Kind | What it replaces | What it revokes |
| --- | --- | --- |
| `os-ca` | The OS CA and the node CA, and every node certificate | Every client file issued before |
| `kubernetes-ca` | The Kubernetes CA, the front-proxy CA and the etcd CA, and every certificate they issued | Every kubeconfig issued before |
| `service-account-key` | The key the API server signs service-account tokens with | Tokens signed with the old key |
| `encryption-key` | The key the API server encrypts Secrets in etcd with | Nothing; old data is re-encrypted |

Rotate when a key may have leaked, when someone who held a client file or a kubeconfig should
no longer have access, or on a schedule well before a CA expires: the OS CA and the Kubernetes
CAs are valid for ten years, and status warns a year ahead.

## Run a rotation phase by phase

A rotation moves through four phases, accept, switch, refresh and finish, and every node
confirms a phase before the next one starts;
[Certificates](../concepts/certificates.md#how-is-a-ca-or-key-rotated) explains what each phase
changes. A rotation pauses where you have work to do. The OS CA and the Kubernetes CAs pause after
accept, so you can hand out client files or kubeconfigs that trust both CAs before servers
present certificates of the new one. Every kind pauses after refresh; `--finish` runs the last
phase. In between, `--resume` continues:

```sh
chalkctl rotate kubernetes-ca
chalkctl rotate kubernetes-ca --resume
chalkctl rotate kubernetes-ca --finish
```

The secrets file records the rotation's phase. chalkctl rewrites it in place after every step,
keeping the previous version as `secrets.age.prev`, and rewrites `secrets.pub.json` beside it.
An encrypted file is encrypted again to the recipients it records; `--recipient` replaces them.
`--out` and `--public-out` write new files instead and leave the old ones as they are; continue
the rotation with `--secrets=<new-file>` then, because only the new file knows its phase. One
rotation runs at a time, and one chalkctl command at a time changes the file, holding
`secrets.age.lock`.

A phase that cannot finish, because a node is down or a step timed out, stops with what failed
and the command to continue:

```text
chalkctl: the accept phase of the rotation of the OS CA stopped: w3: ...; once that is fixed, continue with chalkctl rotate os-ca --resume
```

A phase applied again redoes nothing that is done, so `--resume` does no harm.
`--timeout=<duration>` sets how long each node has for a phase, 10 minutes by default.

## Rotate the OS CA

The OS CA is the root every node and client trusts. Rotating it replaces the node CA too and
gives every node a new certificate.

```console
$ chalkctl rotate os-ca
...
secrets.age records the rotation of the OS CA; the previous version is secrets.age.prev; secrets.pub.json holds the new public half
os-ca: the accept phase
  cp1 trusts the 2 OS CAs of the secrets file
  ...
os-ca: every node applied the accept phase
Every node trusts the old and the new OS CA. Issue new client files now with chalkctl config new: they carry both OS CAs and work throughout the rotation and after it. Client files from before the rotation stop verifying the nodes from the switch on and are refused at the finish. Then continue with chalkctl rotate os-ca --resume, which makes the new OS CA issue.
```

At this pause, issue a new client file for everyone who keeps access, with `chalkctl config new`.
Then resume. The switch gives the control planes the new node CA, and the refresh gives every
node a certificate of it:

```console
$ chalkctl rotate os-ca --resume
...
  w1 serves a certificate of the new node CA
...
Every node serves a certificate of the new node CA and trusts the old and the new OS CA. Client files from before the rotation cannot verify the nodes any more and are refused after the finish: issue new ones with chalkctl config new. Build images and installer media again from secrets.pub.json; older ones trust the old OS CA alone, and installing from them fails. Then remove the old OS CA with chalkctl rotate os-ca --finish.
```

At this pause, commit the new `secrets.pub.json` and rebuild the [installer](../reference/glossary.md#installer) and any images you
keep for installs: images carry the OS CA that [maintenance mode](../reference/glossary.md#maintenance-mode) accepts. Running nodes need no
new image, but the next image you build carries the new OS CA, so it needs a new version like any
other change. Then finish:

```sh
chalkctl rotate os-ca --finish
```

Every node now trusts the new OS CA alone. Client files issued during the rotation still trust
the old OS CA as well when they verify nodes, so whoever holds the old key could pose as a node
to them; issue them once more after the finish to close that.

## Rotate the Kubernetes CAs

`kubernetes-ca` replaces the cluster's three Kubernetes CAs together. Control planes take each
phase one at a time, each restarting its static pods on the new files and waiting until etcd has
every member healthy again, so etcd keeps its quorum. Workers follow.

At the pause after accept, chalkos's add-ons and the workloads of
[`chalkos.cluster.manifests`](../reference/options.md#chalkosclustermanifests) have restarted and
trust both CAs. Restart your own workloads that talk to the API server, so they trust both CAs
too; pods started from then on do. Issue new kubeconfigs with `chalkctl kubeconfig`. Then
resume: the switch makes the new CAs issue, and the refresh gives the kubelets new client and
serving certificates.

At the pause after refresh, issue kubeconfigs again for anyone who did not get one at the first
pause, and finish. As with the OS CA, kubeconfigs issued during the rotation still trust the old
CA; issue them again after the finish.

## Rotate the service-account key

The API server signs service-account tokens with this key. The rotation runs through accept,
switch and refresh without stopping; the refresh restarts chalkos's add-ons, which get tokens of
the new key at once. Kubelets renew the tokens of other pods within the hour after the switch, so
the rotation pauses and names the time from which you can finish. It also names the Secrets of
type `kubernetes.io/service-account-token` that hold tokens of the old key: nothing signs those
again, so recreate them before you finish.

`--finish` before that hour has passed is refused, because it would invalidate tokens that pods
still use; `--finish --force` does it anyway.

## Rotate the encryption key

The API server encrypts Secrets in etcd with this key. The refresh has a control plane rewrite
every encrypted object, so it is stored under the new key, and checks that etcd holds none under
the old one. On a large cluster the rewrite can take longer than `--timeout`; the error says so,
and `--resume` with a longer timeout repeats it without harm, writing back unchanged the
objects it rewrote already. `--finish` is refused while etcd holds objects under the old key,
because removing the key would lose them.

## Revoke access

[chalkd](../reference/glossary.md#chalkd) and the API server check no revocation list. Access ends when what the certificate
chains to is no longer trusted:

- To revoke a client file, rotate the OS CA, and issue new client files at its first pause to
  everyone else.
- To revoke a kubeconfig, rotate the Kubernetes CAs, and issue new kubeconfigs to everyone else.
- To revoke a service-account token, delete its ServiceAccount or Secret in Kubernetes; to
  invalidate every token at once, rotate the service-account key.

Short `--ttl` values for client files and kubeconfigs let access lapse by itself, without a
rotation.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) lists what a node trusts, by the first
16 hexadecimal digits of each fingerprint. A [worker](../reference/glossary.md#worker) in the middle of an OS CA rotation trusts
two OS CAs:

```console
$ chalkctl status w1
...
trust:
  OS CA          e7b462f19ed536c1, 28d451c4085038d7
  Kubernetes CA  980328b6f68680b0
...
```

After the finish, each entry shows one fingerprint again. A control plane also lists the
front-proxy CA, the etcd CA, the service-account keys and the encryption keys, and marks the
value that issues or signs with `(issues)`. The `certificates:` section shows each certificate's
expiry, and the node certificate's line names a renewal that is failing.

## If something goes wrong

- `another chalkctl command is changing secrets.age; wait for it to end, then run this one again`:
  another command holds the lock, perhaps a rotation still waiting for a node.
- `a rotation of the OS CA runs, in its accept phase; continue it with chalkctl rotate os-ca --resume or --finish`:
  a rotation started earlier is not finished. Finish it before you start another or run
  `chalkctl node-ca rotate`.
- `secrets.age changed since chalkctl read it and was left as it is`: something else, an editor
  or a `git checkout`, changed the file during the command. Look at what changed it, then run
  the command again.
- A node unreachable for good blocks every phase. Remove it from the cluster definition, or
  reinstall it, before you resume.

## What next

- [Certificates](../concepts/certificates.md) and [Security model](../concepts/security.md)
  explain what each CA and key protects.
- [Recover a node](recover-node.md) covers a node whose certificate expired while it was off.
- [`chalkctl rotate`](../reference/cli/chalkctl_rotate.md) lists every flag.
