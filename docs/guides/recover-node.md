---
title: "Recover a node"
description: "Bring back a node that does not unlock, boot, renew or rejoin"
---

# Recover a node

This guide brings back a node that is stuck: its disks do not unlock, its new image fell back,
its certificate expired, or it cannot rejoin etcd. Each section starts from what you see, on
the console or in [`chalkctl status`](../reference/cli/chalkctl_status.md), names the cause and
gives the fix. The last resort, reinstalling the node, has its own section, and so has the loss
of the [secrets file](../reference/glossary.md#secrets-file).

## Before you begin

- The secrets file, for most fixes here. A section says when a
  [client file](../reference/glossary.md#client-file) is enough.
- Access to the node's console, through its BMC or in person, for the sections that start there.
- [Troubleshooting](troubleshooting.md), when you do not yet know which of these situations you
  are in.

## The console asks for a passphrase at boot

The node stops early in the boot and systemd-cryptsetup asks on the console for the passphrase
of `state`, and later of `var` and of each encrypted volume. The
[TPM](../reference/glossary.md#tpm) did not unseal the keys of
[STATE](../reference/glossary.md#state) and [VAR](../reference/glossary.md#var), because
[PCR 7](../reference/glossary.md#pcr-7), which the keys are sealed to, changed. Common causes:

- [Secure Boot](../reference/glossary.md#secure-boot) was turned off, or its keys changed: a new PK, KEK or [db](../reference/glossary.md#db-and-dbx), or a dbx update that
  came with a firmware update.
- The image was signed with another db key than the one the node was installed with.
- The TPM was cleared, or the disk moved to another machine.

The node stays at the prompt; it does not fall back to
[maintenance mode](../reference/glossary.md#maintenance-mode), because that would let anyone
with physical access give it a new [identity](../reference/glossary.md#identity). A boot that waits at the prompt also never
reaches the [health check](../reference/glossary.md#health-check): after an upgrade, the node
stays in that boot until you unlock it or reset the machine, and only then does the boot loader
try the new image again or fall back.

Get the node's [recovery key](../reference/glossary.md#recovery-key):

```sh
chalkctl recovery-key <node>
```

`<node>` is the node's name. The key is eight groups of eight letters, separated by dashes. Type
it at each prompt; the node then boots on. A node whose fallback is a password takes that
password instead.

The recovery key is derived from the secrets file, the cluster's name and the node's name, so it
is the same for the node's whole life, also across reinstalls. Whoever has read it can unlock
that node's disks with physical access for good. Keep the console session private.

The prompt comes back at every boot until PCR 7 matches the sealed keys again. chalkos has no
command that seals the keys to the new PCR 7, so there are two ways out:

- Undo the change: turn Secure Boot back on, restore the previous keys or boot an image signed
  with the original db key. The keys unseal again at the next boot.
- Accept the change and reinstall the node, as in [Reinstall a node](#reinstall-a-node). The
  installer seals the new keys to the PCR 7 the node has now.

A node whose fallback is `none` has no second key. STATE and VAR stay locked, so the only way
back is to undo the change or reinstall. An extra volume that does not unlock stays unmounted
while the rest of the node runs, and
[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) recreates it empty.

## The node fell back after an upgrade

`chalkctl status` shows the node running its previous image, with the failed boot's log:

```text
image 1.4.0, booted from chalkos_1.4.0.efi
upgrade to 1.5.0 failed: rolled back to 1.4.0
  ...
```

The new image did not become healthy within its tries, and systemd-boot booted the previous one.
The node is healthy on the old image and stays cordoned. Find the cause in the logged lines, fix
it in a new version and upgrade again, as in
[Upgrade a cluster](upgrade-cluster.md#if-a-node-rolls-back).
[Boot, health and rollback](../concepts/boot-and-rollback.md) explains the mechanism.

## The node certificate expired

chalkctl refuses the node's certificate as expired, so every command against the node fails, while
the node itself still runs. A node renews its
[node certificate](../reference/glossary.md#node-certificate) once two thirds of its year have
passed: a [control plane](../reference/glossary.md#control-plane) signs its own with its node CA,
and a worker asks a control plane at the
[cluster endpoint](../reference/glossary.md#cluster-endpoint) on port 50000. A certificate therefore
expires only when the node could not renew for four months: it was switched off, a worker could not
reach the endpoint, or the node has no Kubernetes and nobody renewed it by hand. Before the expiry,
its status showed why:

```text
certificates:
  node  expires 2027-03-02  renewal failing: ...; expires 2027-03-02T10:00:00Z
```

[`chalkctl node renew`](../reference/cli/chalkctl_node_renew.md) issues and delivers a new
certificate, also to a node whose certificate expired:

```console
$ chalkctl node renew w1
w1 serves a new node certificate; it expires 2027-10-10T09:12:44Z
```

For this command alone, chalkctl verifies the node's expired certificate as of its start date,
so it trusts the node's old key; [Certificates](../concepts/certificates.md#what-if-a-certificate-expired)
explains what that risks. Fix the reason the renewal failed too, or the node expires again next
year.

## The kubelet certificate was lost with VAR

A [worker](../reference/glossary.md#worker) whose [VAR](../reference/glossary.md#var) lost its data falls back to the kubelet
certificate in its [Kubernetes share](../reference/glossary.md#kubernetes-share) on
STATE, which it received at install. When that one expired too, the kubelet cannot join, and
`chalkctl status w1` says:

```text
certificates:
  ...
  kubelet client  expires 2026-09-14  expired; deliver a new one with chalkctl apply-identity w1 --kubernetes-share
```

Deliver a new share with a new kubelet certificate:

```sh
chalkctl apply-identity w1 --kubernetes-share
```

## A control plane cannot rejoin etcd

A control plane's Kubernetes line in `chalkctl status` names what holds it back, and the fix:

| Status | Cause | Fix |
| --- | --- | --- |
| `preparation failed: pinned address ... is not present` | The node booted without one of the addresses it was pinned to when it became an [etcd member](../reference/glossary.md#etcd-member). | Restore the address and reboot. Or take the node out of etcd and reinstall it: `chalkctl etcd leave <node> --force` removes its member through the other members, as does `chalkctl etcd remove-member <node>`. |
| `joining the cluster at ...: etcd has a member <node> already; remove it with chalkctl etcd remove-member <node>` | The node was reinstalled without leaving etcd, and its old member is still there. | Run the command the status names; the node joins on its own. |
| `left etcd; reinstall the node to join the cluster again` | The node left etcd with `chalkctl etcd leave`. | Reinstall it. |
| `etcd data missing: restore etcd or reinstall the node` | The node is an etcd member, but VAR holds no etcd data, because VAR lost its data. | chalkos cannot restore etcd. Remove its member with `chalkctl etcd remove-member <node>` and reinstall it. |
| `joining the cluster at ...: the node's etcd member was removed while joining; run chalkctl etcd leave and reinstall the node` | An operator removed the member while the node was joining. | Run `chalkctl etcd leave <node>`, then reinstall it. |

[Run a highly available control plane](ha-control-plane.md) explains joining, leaving and the
quorum guard on these commands. They need an admin client file or the secrets file.

## A storage change is refused

`chalkctl apply-identity` fails with `the identity changes storage destructively: ...`, and the
node keeps its previous identity in full. The change shrinks, reformats, re-encrypts, moves or
removes a volume, which would lose its data. Reset that one volume with
`chalkctl storage reset <node> <volume>`, which deletes its data, then deliver the identity
again. VAR, a removed volume and a volume that moves to another disk cannot be reset; those
changes need a reinstall. [Add storage volumes](storage-volumes.md#reset-a-volume) has the
details.

## An extra disk is missing

The node boots and runs, but `chalkctl status` lists a volume as `missing` and names the disk:

```text
VOLUME    DISK      MOUNT POINT        STATE
longhorn  longhorn  /var/lib/longhorn  missing
var       system    /var               mounted
disk longhorn: pinned disk ... is missing
```

The disk the volume was created on is gone: removed, dead or behind a controller that did not
come up. [chalkd](../reference/glossary.md#chalkd) finds a disk by its pinned WWN, serial number and path, so another disk in its
place is not used, even if it is empty. Put the disk back, or fix its controller, and reboot. A
replacement disk needs the node reinstalled.

## The node is in maintenance mode

The console shows chalkd's certificate fingerprint and the node's addresses, and
`chalkctl status` fails to verify the node. The node boots an image that has no installation to
load:

- It booted the [installer](../reference/glossary.md#installer) again because the installer
  medium is still attached and comes first in the boot order. Remove the medium or fix the
  boot order, and reboot.
- The install did not finish. Run `chalkctl install` again; it continues an unfinished install
  of the node's role on the same disk.

A node in maintenance mode answers `chalkctl logs` and `chalkctl reboot` with `--fingerprint`
or `--insecure`, so you can read why an install failed:

```sh
chalkctl logs <node> --insecure --unit=chalkd.service
```

## Reinstall a node

Reinstalling gives a node a new disk layout, new keys sealed to its TPM and a fresh VAR, from
the [cluster definition](../reference/glossary.md#cluster-definition). Everything on its disks is lost, so move workloads off first.

1. For a control plane, take it out of etcd first, with `chalkctl etcd leave <node>`, so the
   others keep a correct membership. For a worker, drain it with `kubectl drain <node>`.
2. Boot the machine into the installer through its BMC or boot menu. An installed node runs no
   maintenance mode, so chalkctl cannot reach it for an install until it does.
3. Install it, letting the installer replace what the disk holds:

    ```sh
    chalkctl install <node> --fingerprint=<fingerprint> --wipe-disk
    ```

    `<fingerprint>` is the one the console shows.

The node gets the same name, the same recovery key and new certificates. A control plane joins
etcd as a new member; a worker's kubelet registers its Node again. Uncordon a drained worker with
`kubectl uncordon <node>` once it is Ready.

## The secrets file is lost

Without the secrets file, and without a backup of it, the cluster keeps running, but much of
its management is gone for good:

| Still works | Gone |
| --- | --- |
| Commands with existing client files, within their roles: status, logs, disks, reboot, upgrade, bootstrap, the etcd commands, and storage reset where no TPM-encrypted volume is created: on nodes whose fallback is not the recovery key, or of a volume without encryption | Installing or reinstalling a node, `apply-identity` |
| Node certificate renewal by the control planes, until the [node CA](../reference/glossary.md#node-ca) expires | `chalkctl node renew`, `node-ca rotate` and every `rotate` |
| Kubelet certificate renewal | New client files and kubeconfigs |
| Existing kubeconfigs, until they expire | Recovery keys: a node whose TPM stops unsealing cannot be unlocked |

The client files and kubeconfigs expire within their validity, a year by default, and nothing
can issue new ones. Plan a move to a new cluster before then: generate new secrets, build new
images and install new nodes, and move the workloads across. Back up the secrets file so this
never happens; [Plan a production cluster](production-cluster.md#protect-the-secrets-file) says
how.

## What next

- [Troubleshooting](troubleshooting.md) starts from symptoms.
- [Boot, health and rollback](../concepts/boot-and-rollback.md),
  [Certificates](../concepts/certificates.md) and [Storage and encryption](../concepts/storage.md)
  explain the mechanisms these fixes act on.
- [Run a highly available control plane](ha-control-plane.md) covers etcd membership.
