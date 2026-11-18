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

The node stops early in the boot and asks for the passphrase of `state`.
[The console asks for a passphrase at boot](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
gives the causes, the recovery key that unlocks the node and how to make the prompt go away.

## The node is in maintenance mode after the install

The console shows a fingerprint again, and `chalkctl status` cannot verify the node. The machine
booted the installer medium again, or the install did not finish.
[The node is in maintenance mode](recover-node.md#the-node-is-in-maintenance-mode) has both
fixes.

## The node stops in the initrd, or a NIC or disk is missing

The console shows the node stopping in the initrd because it finds no system disk. Or a network
card is missing from `chalkctl node diag w1 links`, or a disk from `lsblk` in a shell on the node.
`lsblk` and `dmesg` are on images built with `chalkos.debug.tools`, as
[Reach the node's tools through kubectl debug](status-and-logs.md#reach-the-nodes-tools-through-kubectl-debug)
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
[Run the image's diagnostics with chalkctl node diag](status-and-logs.md#run-the-images-diagnostics-with-chalkctl-node-diag)
lists every diagnostic.

## A volume is missing or not mounted

The volume table of `chalkctl status` shows `missing` or `not mounted`, and a line
`disk <name>: <problem>` names the disk. A pinned disk is gone, carries other data, or sits behind
a controller without a driver. [Add storage volumes](storage-volumes.md#if-something-goes-wrong)
covers each case.

## The node refuses a storage change

`chalkctl config apply` fails with `the config takes chalkctl storage reset: ...` or
`the config takes a reinstall of w1: ...`, and the node keeps its previous config. The change
shrinks, reformats, re-encrypts, moves or removes a volume.
[If it fails](change-config.md#if-it-fails) on the config page lists what to do.

## A config rollback or trial is refused

`chalkctl config rollback`, `config confirm` or `config apply --try` stops before the node changes
anything.

- `Volume scratch: the config removes it; only a reinstall of node1 removes a volume` from
  `config rollback`: the previous config lacks a volume that the last delivery added. Keep the
  current config, or reinstall the node.
- `node1 keeps no previous config of the image it runs; it keeps one once config apply replaced the
  config the image was installed or upgraded with`: an install or an upgrade wrote the config, so
  there is nothing to roll back to yet.
- `the config changes the node's storage, volume <name>: <reason>, which a rollback cannot undo;
  deliver it without a trial`: deliver the config without `--try`.
- `node1 has no open trial to confirm; it runs config <version>`: no trial is open on the node,
  so there is nothing to confirm.

## A config on trial rolled back

`chalkctl config apply --try` or `chalkctl config confirm` exits non-zero, and the node runs the
config from before the trial again.

- `node1: config <version> was not confirmed within its trial: ...`, with
  `node1 rolled back to config <previous-version> at the trial's deadline`: the node did not
  confirm the config in time. The error ends with the last reason, such as
  `the node is not healthy: units failed: demo.service`. Fix the cause and try again.
  `the answer to its delivery did not come` means the config cut chalkctl off.
- `node1 runs config <previous-version> again: config <version> was rolled back`: the image's
  layers did not find the node serving by the deadline. On a Kubernetes node whose Node is not
  Ready for another reason than a missing CNI, deliver a config meant to fix the node without
  `--try`: the node then applies the config for good, and chalkctl waits for no layer and no
  health check. A config that changes the API server's version goes to a control plane on trial even
  then, so mend the node with a config that keeps the version first.
- `chalkctl: warning: node1: units failed already: late.service; the trial rolls back at its deadline unless they recover, the image ignores them or --ignore-unit leaves them out`:
  a unit failed before the trial, and any failed unit keeps the node from confirming. Leave it
  out of the trial's check with `--ignore-unit`, one unit per flag:
  `chalkctl config apply node1 --secrets secrets.age --try 5m --ignore-unit late.service`.

[chalkctl confirms a trial over a new connection](../how-it-works/node/config.md#chalkctl-confirms-a-trial-over-a-new-connection)
explains what the confirmation waits for.

## A staged image kept its earlier config

`chalkctl config apply` fails or warns after the running image took the config, and the
`staged config` line of `chalkctl status` is not the inventory's.

- `the image <hash> staged in the inactive slot: ... no fallback secret was sent; ...`: the staged
  image's storage needs the fallback secret and the running image's does not. Deliver the config to
  the staged image with `chalkctl config apply --staged node1 --secrets secrets.age`.
- `the node runs config <version> (<what it did>), but writing the config slot of the image <hash> staged in the inactive slot failed, so it keeps config <version>: ...`:
  the running image took the config, and the staged image kept its earlier one. Run the command
  again to write it.
- `config <version> is confirmed, but the image <hash>, staged in the inactive slot when the trial began, is the running image now, ...`:
  the node booted into the staged image during the trial, so the running image's config slot never
  got the confirmed config. Run `chalkctl config apply node1 --secrets secrets.age` to write it.
- `config <version> is confirmed, but the confirmation that ended its trial stopped before it wrote the config slot of the image <hash> ...`:
  chalkd or the node stopped while it confirmed the trial, and the staged image kept its earlier
  config. Run `chalkctl config apply node1 --secrets secrets.age` to write it.

## An upgrade refuses the pending config

`chalkctl upgrade` stops before it stages the image, and the node keeps its pending config.

- `the pending config <version> does not suit the image: ...`: the image the upgrade installs
  refused the config the node held pending. Change the files and run
  `chalkctl config apply --staged node1` again, or remove the pending config with
  `chalkctl config discard node1`.
- `the pending config <version> does not suit the image: ...; a pending config is kept without the fallback secret, ...`:
  the pending config adds an encrypted volume, which needs the fallback secret. Upgrade with the
  config from the inventory and the secrets file.

## An upgrade rolled back or stopped

`chalkctl upgrade` stops at a node whose new image did not become healthy. The status shows
`upgrade to <new> failed: rolled back to <old>` and the failed boot's journal lines.
[A node rolled back during the upgrade](upgrade.md#a-node-rolled-back-during-the-upgrade) says
how to go on. The run also stops with one of these messages:

- `the boot of chalkos_2+2-1.efi has not been found healthy yet; upgrade once it is, ...`: the node
  runs an image whose health check still runs. Installing another would remove the image it falls
  back to. Wait for the check, at most the health timeout per try.
- `Secure Boot would refuse the UKI: ...`: the image is unsigned, or signed with a key the
  firmware's db does not hold. Sign the build with `chalkbuild sign` and the db key.
- `node3 runs an image built for kvm, but its image node-metal is built for metal; changing a node's
  platform is a reinstall`: the inventory names a build of another platform for the node. Fix the
  inventory, or reinstall the node.
- `the config of <node>: ...`: the new image does not accept the node's config, such as a document
  of a kind the image no longer declares. Change the node's files.
- `upgrade needs the operator role; the reader role may not upgrade nodes`: the client file has the
  reader role. Use an operator or admin client file.
- `config not delivered: the operator role cannot deliver config; each node gives the image its pending config, else carries its own over`:
  the run used an operator client file. Delivering config with the image needs the secrets file or
  an admin client file, and so does `--diff`.
- `activate: no image is staged in the inactive slot` or
  `activate: the image staged in the inactive slot is <a>, not <b>`: `chalkctl node activate`
  found no image staged, or another one than the inventory or `--image` names for the node. Stage
  the image with `chalkctl upgrade --nodes <node> --until stage` first, with the same `--image`
  when you activate one.
- `node3: its boot of <image> settled and is not healthy: ...`: the node runs the image, but its
  boot failed the node's health checks for a minute in a row, after systemd finished starting it
  and the image's health timeout passed. The checks are that chalkd serves and then those of the
  image's layer: on Kubernetes, the etcd member, the API server and the kubelet of a control
  plane, or the kubelet and the Node of a worker. A node whose layer does not decide fails on a
  failed unit or an identity that was not applied. The message names the check that failed. A
  boot of a blessed entry is not counted, so the node does not fall back. `chalkctl status node3`
  lists failed units, and `chalkctl logs node3` shows the boot's journal. A boot that is still
  starting at `--timeout` ends with
  `node3: waiting for <image> to be found healthy: its boot is not healthy yet: ...` instead. When
  the node's status gave no boot ID before the reboot, chalkctl cannot tell the new boot from the
  old one and waits until `--timeout`, ending with `its boot settled and is not healthy: ...`.
- `node3: activate: ...; <image> boots next all the same, so it stays cordoned: ...`: the node's
  activation failed after it made the image boot next. Uncordoned, the node would boot the image
  at its next reboot without a drain, so it stays cordoned. `chalkctl node reboot node3` boots the
  image and uncordons the node.
- `no node keeps a previous image to roll back to: ...`: `--rollback` found no node with an image
  to boot again. A node keeps none after an install, after an upgrade or rollback it fell back
  from, and while an image staged or activated with `--until` holds its inactive slot. Make a
  staged image boot with `chalkctl node activate` and `chalkctl node reboot`.
- `the nodes roll back to different images: node1 to <a>; router to <b>; roll them back separately with --nodes`:
  the nodes ran different images before, such as control planes and workers on images of their
  own. Roll each group back with its own `--nodes`.
- `w2 does not run <old>, which the other nodes roll back to, and keeps no previous image: ...; leave
  it out with --nodes`: the node was installed with the image the others run now and never
  upgraded. Leave it out with `--nodes`.

With the Kubernetes layer, the run also stops at a control plane whose reboot would cost etcd its
quorum, and at a drain that does not finish.
[Upgrade Kubernetes](../kubernetes/upgrade-kubernetes.md#if-something-goes-wrong) covers those.

## The config line says not the inventory's

`not the inventory's; chalkctl config apply w1 delivers it` means the node's files changed since
the last delivery. Preview and deliver the change, as
[Change a node's config](change-config.md) describes.

`not the inventory's; chalkctl upgrade --nodes w1 delivers it with the image <image>` means the
node also runs another image than the inventory's. `config apply` would check the files against
the image the node runs, which may refuse them. Upgrade the node, as
[Upgrade nodes](upgrade.md) describes; the upgrade delivers the config with the image.

`... delivers it if the image <image> in the inactive slot, which is not the inventory's, accepts
it too` means an image the inventory no longer names is staged or active on the node. A delivery
writes that image's config slot too, and that image checks the config first. When it refuses,
deliver the config once an upgrade to a new image replaced it.

## Status warns about a certificate

The certificates section has a third column, such as `less than a third of its lifetime remains`
or `renewal failing: ...`.
[Read the certificates section](status-and-logs.md#read-the-certificates-section)
says what each warning asks for. An expired node certificate makes every command against the node
fail. [The node certificate expired](recover-node.md#the-node-certificate-expired) brings it back.

## The clock is not synchronised

The status says `time: not synchronised; certificates are checked against this clock`. chrony
reached none of its servers. With the default servers, NTS needs the node to reach them on TCP
port 4460 and UDP port 123. A network that blocks both needs internal servers in the node's
`TimeSync` document, as
[Name the time servers](node-config.md#name-the-time-servers) shows.

## A unit failed

The status ends with `failed unit <unit>`. Read the unit's journal:

```sh
chalkctl logs w1 --unit=<unit>
```

`<unit>` is the unit the status names.
[Follow a unit's journal with chalkctl logs](status-and-logs.md#follow-a-units-journal-with-chalkctl-logs)
lists the units of chalkos.

## Image pulls fail

An install or upgrade from a registry fails, with an error that names the source the pull ended at.
`after the mirror <endpoint>: <error>` follows for each endpoint chalkd passed over. Compare the
`Registry`'s `host` with the host as the image reference names it, port included. An `http://`
endpoint needs a `Registry` of its host with `insecure: true`.
[Registries and mirrors](registries.md) covers credentials and mirrors.

!!! note "With the Kubernetes layer, pods pull with the same documents"

    Pods that stay in `ErrImagePull` or `ImagePullBackOff` pull through containerd. Read
    `containerd.service`'s log for the URL that failed. `chalkos-registry-mirrors.service` writes
    the endpoints to `/run/containerd/certs.d/<registry>/hosts.toml`, and
    `chalkos-registry-credentials.service` writes the credentials. Their logs show when they could
    not. The kubelet asks for `registry.example.com:5000/app` only with a `Registry` of
    `registry.example.com:5000`. A cluster without internet access needs every image in a mirror
    or on every endpoint of its cluster registry, which `chalkctl registry list` checks.

## A Kubernetes symptom

With the Kubernetes layer, symptoms of Kubernetes show in the status's Kubernetes line or in
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
