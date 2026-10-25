---
title: "Troubleshooting"
description: "Find the cause of a node's symptom and the page that fixes it"
---

# Troubleshooting

This page is for an operator who sees a node misbehave and does not know why yet. Each section
starts from a symptom, names the likely cause and links the page with the fix. Most symptoms show
in `chalkctl status` first. [Status, logs and the console](status-and-logs.md) explains each of its
lines.

## Before you begin

You need a reader client file or the secrets file, for `chalkctl status` and `chalkctl logs`. For
a symptom that shows before chalkd serves, you need the node's console: its BMC, a screen, or
`chalklab console` in a lab.

Run `chalkctl status` on the node first:

```sh
chalkctl status w1
```

Then find the symptom below.

## The console asks for a passphrase

The TPM did not unseal the keys of STATE, usually after a change to Secure Boot or the firmware.
Type the node's recovery key from `chalkctl recovery-key`.
[The console asks for a passphrase at boot](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
explains the causes and how to make the prompt go away.

## The node is in maintenance mode after the install

The console shows a fingerprint again, and `chalkctl status` cannot verify the node. The machine
booted the installer medium again, or the install did not finish.
[The node is in maintenance mode](recover-node.md#the-node-is-in-maintenance-mode) has both
fixes.

## The node stops in the initrd, or a NIC or disk is missing

The console shows the node stopping in the initrd because it finds no system disk. Or a network
card is missing from `chalkctl node diag w1 links`, or a disk from `lsblk` in a shell on the node.
`lsblk` and `dmesg` are on images built with `chalkos.debug.tools`, as
[Debug tools reach the node through kubectl debug](status-and-logs.md#debug-tools-reach-the-node-through-kubectl-debug)
describes. The image lacks the device's driver, or the firmware the driver loads, and `dmesg`
names a firmware file that failed to load. A disk controller's driver must also be in the initrd.
The image author adds it, as [Add software, drivers and kernel modules](../build/add-software.md)
describes.

## The node cannot reach an address

An image pull, a time server or a peer times out, and the journal names an address. The node's
diagnostics narrow it down without a shell on the node. Check the route, then the address, then
the port, then the name:

```sh
chalkctl node diag w1 route-get address=2001:db8::5000
chalkctl node diag w1 ping address=2001:db8::5000
chalkctl node diag w1 tcp-connect address=2001:db8::5000 port=443
chalkctl node diag w1 dns-query name=registry.example.org type=AAAA
```

`route-get` names the interface and gateway the kernel picks for the address. It fails with
`RTNETLINK answers: Network is unreachable` when no route covers the address. A missing route or
a wrong interface comes from the node's network documents, which
[Write a node's config](node-config.md) covers. A route that is right while `ping` and
`tcp-connect` fail points at the network past the node, or at a firewall. `nft-ruleset` shows the
node's own. `ping`, `dns-query`, `tcp-connect` and `nft-ruleset` need an operator client file.
[chalkctl node diag runs the image's diagnostics](status-and-logs.md#chalkctl-node-diag-runs-the-images-diagnostics)
lists every diagnostic.

## A volume is missing or not mounted

The volume table of `chalkctl status` shows `missing` or `not mounted`, and a line
`disk <name>: <problem>` names the disk. A pinned disk is gone, carries other data, or sits behind
a controller without a driver. [Add storage volumes](storage-volumes.md#if-something-goes-wrong)
covers each case.

## The node refuses a storage change

`chalkctl apply-config` fails with `the config takes chalkctl storage reset: ...` or
`the config takes a reinstall of w1: ...`, and the node keeps its previous config. The change
shrinks, reformats, re-encrypts, moves or removes a volume.
[A refusal names what the change takes](change-config.md#a-refusal-names-what-the-change-takes)
lists what to do.

## An upgrade rolled back or stopped

`chalkctl upgrade` stops at a node whose new image did not become healthy. The status shows
`upgrade to <new> failed: rolled back to <old>` and the failed boot's journal lines.
[If something goes wrong](upgrade.md#if-something-goes-wrong) on the upgrade page lists the
messages and their fixes.

On the shipped variant, the run also stops at a control plane whose reboot would cost etcd its
quorum, and at a drain that does not finish.
[Upgrade Kubernetes](../kubernetes/upgrade-kubernetes.md#if-something-goes-wrong) covers those.

## The config line says not the inventory's

`not the inventory's; chalkctl apply-config w1 delivers it` means the node's files changed since
the last delivery. Preview and deliver the change, as
[Change a node's config](change-config.md) describes.

## Status warns about a certificate

The certificates section has a third column, such as `less than a third of its lifetime remains`
or `renewal failing: ...`.
[The certificates section warns before an expiry](status-and-logs.md#the-certificates-section-warns-before-an-expiry)
says what each warning asks for. An expired node certificate makes every command against the node
fail. [The node certificate expired](recover-node.md#the-node-certificate-expired) brings it back.

## The clock is not synchronised

The status says `time: not synchronised; certificates are checked against this clock`. chrony
reached none of its servers. With the default servers, NTS needs the node to reach them on TCP
port 4460 and UDP port 123. A network that blocks both needs internal servers in the node's
`TimeSync` document, as
[TimeSync names the time servers](node-config.md#timesync-names-the-time-servers) shows.

## A unit failed

The status ends with `failed unit <unit>`. Read the unit's journal:

```sh
chalkctl logs w1 --unit=<unit>
```

`<unit>` is the unit the status names.
[chalkctl logs prints a unit's journal](status-and-logs.md#chalkctl-logs-prints-a-units-journal)
lists the units of chalkos.

## Image pulls fail

An install or upgrade from a registry fails, with an error that names the source the pull ended at.
`after the mirror <endpoint>: <error>` follows for each endpoint chalkd passed over. Compare the
`Registry`'s `host` with the host as the image reference names it, port included. An `http://`
endpoint needs a `Registry` of its host with `insecure: true`.
[Registries and mirrors](registries.md) covers credentials and mirrors.

!!! note "On the shipped variant, pods pull with the same documents"

    Pods that stay in `ErrImagePull` or `ImagePullBackOff` pull through containerd. Read
    `containerd.service`'s log for the URL that failed. `chalkos-registry-mirrors.service` writes
    the endpoints to `/run/containerd/certs.d/<registry>/hosts.toml`, and
    `chalkos-registry-credentials.service` writes the credentials. Their logs show when they could
    not. The kubelet asks for `registry.example.com:5000/app` only with a `Registry` of
    `registry.example.com:5000`. A cluster without internet access needs every image in a mirror.

## A Kubernetes symptom

On the shipped variant, symptoms of Kubernetes show in the status's Kubernetes line or in
kubectl: a node with no address, a control plane that waits for bootstrap or does not finish
joining, failing `kubectl logs`, pending kubelet certificate requests, pods that cannot reach each
other, a Node without a pod range, a control plane that refuses the cluster's families, and a
kubelet certificate lost with VAR. [Troubleshoot Kubernetes](../kubernetes/troubleshooting.md)
covers each one.

## What to put in a bug report

A report someone can act on holds:

- the chalkos version, from `flake.lock`, and the image line of `chalkctl status`, which names the
  image's root hash and the revision it was built from;
- the full output of `chalkctl status` for the nodes involved;
- the logs of the units involved, from `chalkctl logs <node> --unit=<unit>`;
- for a boot problem, the console output, from the BMC or `chalklab console <node> -f=false`;
- the config documents and image modules that concern the problem, without `Secret` documents;
- the commands you ran and their full output.

Leave out the secrets file, client files, kubeconfigs and recovery keys. Each grants access to the
cluster.

## What next

- [Status, logs and the console](status-and-logs.md) explains what a node reports.
- [Recover a node](recover-node.md) fixes nodes that do not unlock, boot or renew.
- [Boot, health and rollback](../how-it-works/node/boot-and-rollback.md) explains what the health
  check looks at.
