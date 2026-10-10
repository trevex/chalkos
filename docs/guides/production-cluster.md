---
title: "Plan a production cluster"
description: "Decide the roles, addresses, storage, keys and time sources of a production cluster before installing it"
---

# Plan a production cluster

A production cluster needs decisions that the [quick start](../getting-started/index.md)'s lab
makes for you. A few of them cannot change once the cluster runs: the IP families, the control
planes' addresses, the [secrets file](../reference/glossary.md#secrets-file) and the Secure Boot
signing key. This page takes the decisions in the order you meet them while writing the
[cluster definition](../reference/glossary.md#cluster-definition), each with a recommendation
and what it costs.

## Before you begin

- Run the [quick start](../getting-started/index.md) once, so the commands and the status output
  are familiar.
- Know the machines: their disks, network segments and whether their firmware lets you enroll
  your own Secure Boot keys.
- Read [The cluster definition](../concepts/cluster-definition.md) for how roles, platforms and
  nodes fit together.

## Choose the number of control planes

Use three [control planes](../reference/glossary.md#control-plane). etcd needs a majority of
its voters, so three survive the loss of one machine, and
[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) can reboot them one at a time while
the API server keeps answering.

| Control planes | Survives the loss of | Upgrades and reboots |
| --- | --- | --- |
| 1 | none | The API server is down while the node reboots; `chalkctl upgrade` needs `--allow-downtime`. |
| 2 | none | Both must run for a quorum, so two are less available than one; `--allow-downtime` again. |
| 3 | one | One at a time, with no API downtime. |
| 5 | two | One at a time. Each member adds etcd write latency. |

Four voters tolerate one failure, like three, so an even number buys nothing. A single control
plane is fine for a lab or a cluster whose API may go away during upgrades. Small clusters can
run workloads on the control planes with
[`chalkos.cluster.kubernetes.allowSchedulingOnControlPlanes`](../reference/options.md#chalkosclusterkubernetesallowschedulingoncontrolplanes);
otherwise they are tainted.

[Run a highly available control plane](ha-control-plane.md) walks through installing three.

## Decide how clients reach the API server

The cluster endpoint, [`chalkos.cluster.endpoint`](../reference/options.md#chalkosclusterendpoint),
is the URL kubelets, kubeconfigs and workers use to reach the control planes. Workers also renew
their [node certificates](../reference/glossary.md#node-certificate) through it: chalkd on a
worker connects to the endpoint's host on port 50000, where a control plane's chalkd answers.
So whatever the endpoint points at must reach chalkd on 50000 as well as the API server on 6443.

Two ways make the endpoint survive a control plane's failure:

- A [VIP](../reference/glossary.md#vip), which chalkd holds on one healthy control plane. It
  needs the control planes on one layer-2 segment, because the holder announces the address
  with gratuitous ARP and unsolicited neighbour advertisements. This is the default choice for
  machines in one rack or VLAN. The endpoint is the VIP, or a host name that resolves to it.
- An external load balancer, for control planes in different segments. It must forward port
  50000 too; a balancer that forwards only 6443 leaves workers unable to renew their
  certificates.

The VIP's holder keeps an etcd lease of 10 seconds and checks its API server every 2 seconds;
after three failed checks it resigns. In the HA tests the VIP moved within 30 seconds of its
holder's link going down. Clients with open connections to the old holder can take longer to
notice: in one test a kubelet went more than 40 seconds without reaching the API server.

```nix title="cluster.nix"
{
  chalkos.cluster = {
    name = "prod";
    endpoint = "https://10.0.0.10:6443";
    kubernetes.vip.addresses = [ "10.0.0.10" ];
  };
}
```

[Networking](../concepts/networking.md) explains the election and the address selection.

## Fix the IP families

[`chalkos.cluster.kubernetes.ipFamilies`](../reference/options.md#chalkosclusterkubernetesipfamilies)
lists the cluster's address families, the primary one first: `[ "ipv4" ]` by default,
`[ "ipv4" "ipv6" ]` for dual stack. The families and their order are fixed when the cluster is
created. A control plane compares them with the families it was pinned with and refuses to
prepare Kubernetes when they differ:

```text
the cluster was created with families [ipv4]; ipFamilies is [ipv4 ipv6]; changing the families of a running cluster is not supported
```

A different choice later means a new cluster, so decide now. Pick the pod and service ranges
of each family at the same time
([`podCIDRs`](../reference/options.md#chalkosclusterkubernetespodcidrsipv4) and
[`serviceCIDRs`](../reference/options.md#chalkosclusterkubernetesservicecidrsipv4)), so they
overlap none of your networks.

## Plan the node addresses

Give every node a static address in its `network` definition. Two things depend on it:

- A control plane's addresses are pinned on [STATE](../reference/glossary.md#state) when it
  becomes an [etcd member](../reference/glossary.md#etcd-member), and every later boot waits for
  exactly those addresses. A control plane cannot change its address while it runs; it leaves
  etcd and is reinstalled.
- chalkctl reaches a node at the first static address of its network definition. A node
  without one needs `--endpoint` on every command.

Workers may take their address from DHCP instead and pick it with
[`chalkos.cluster.kubernetes.nodeIP.validSubnets`](../reference/options.md#chalkosclusterkubernetesnodeipvalidsubnets)
or a node's own
[`kubernetes.validSubnets`](../reference/options.md#chalkosnodeskubernetesvalidsubnets). The
cost is the `--endpoint` on each chalkctl command for those nodes.

```nix title="nodes.nix"
{
  chalkos.nodes.cp1 = {
    role = "controlplane";
    storage.system.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001";
    network.networks."10-uplink" = {
      matchConfig.Name = "enp1s0";
      address = [ "10.0.0.11/24" ];
      gateway = [ "10.0.0.1" ];
    };
  };
}
```

Set [`chalkos.cluster.kubernetes.vxlanSourceSubnets`](../reference/options.md#chalkosclusterkubernetesvxlansourcesubnets)
to the ranges your nodes live in. Without it, nodes accept the pod network's VXLAN traffic from
any source.

## Plan disks, storage and encryption

Name the system disk by a path that survives a reordering of the disks, such as
`/dev/disk/by-id/...`, not `/dev/sda`. [VAR](../reference/glossary.md#var), which holds etcd,
containerd's images and the logs, fills the rest of the system disk unless
[`storage.var.size`](../reference/options.md#chalkosnodesstoragevarsize) limits it. Volumes for
local persistent volumes or a storage system go on the system disk or on disks of their own,
picked by path or by a selector of model, serial number, WWN, size and type.
[Add storage volumes](storage-volumes.md) has the details.

Encryption is on by default: STATE, VAR and every volume are LUKS2 volumes whose key the
[TPM](../reference/glossary.md#tpm) seals to [PCR 7](../reference/glossary.md#pcr-7). The
fallback, [`storage.encryption.fallback`](../reference/options.md#chalkosnodesstorageencryptionfallback),
decides what unlocks a volume when the TPM does not:

| Fallback | What unlocks it | Cost |
| --- | --- | --- |
| `recovery-key` (default) | The node's [recovery key](../reference/glossary.md#recovery-key), derived from the secrets file and printed by `chalkctl recovery-key` | Someone types the 64-letter key at the console, through a BMC or in person. |
| `password` | A password you give at install | You keep one more secret per node, outside the secrets file. |
| `none` | Nothing | A volume the TPM does not unseal is reset; for STATE and VAR that means reinstalling the node. |

Set `encryption.mode = "none"` only for machines in a place you trust physically. chalkos then
warns at evaluation, because STATE holds the node's keys unencrypted.

## Decide who holds the Secure Boot key

Every image a node boots is signed with a Secure Boot db key, and the firmware boots it only
when the key's certificate is in db. chalkos does not enroll keys in the firmware: you enroll
the certificate yourself, through the firmware's setup or your vendor's tools, before the first
install. [Sign images for Secure Boot](secure-boot-signing.md) covers the keys and the
enrollment.

Treat the db key as fixed for the cluster's lifetime. PCR 7 measures which db certificate
verified the boot, so an image signed with another key boots, if the firmware trusts it, and
then the TPM refuses to unseal STATE: every node asks for its recovery key. A firmware update
that changes db or dbx has the same effect.

Decide who signs. Whoever runs `chalkctl upgrade --sign-key=<db-key>` holds the key on their
machine; a build machine that signs images and hands them out keeps it in one place. Set
[`chalkos.secureBoot.signerCertificate`](../reference/options.md#chalkossecurebootsignercertificate)
to the db certificate, so `chalkctl upgrade` refuses an image that key did not sign.

## Protect the secrets file

[`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md) writes the secrets file once.
It holds the [OS CA](../reference/glossary.md#os-ca) and [node CA](../reference/glossary.md#node-ca)
with their keys, the Kubernetes CAs and keys, and the secret every node's recovery key derives
from. Whoever holds it can install a node into the cluster, issue themselves any client
certificate and decrypt every node's disks with physical access.

Encrypt it with age to several recipients: each administrator's key, and one backup key kept
offline. Recipients may be age keys, SSH public keys or age plugin recipients, so hardware keys
work through a plugin such as age-plugin-yubikey, whose binary must be on the `PATH` of whoever
runs chalkctl.

```sh
chalkctl gen secrets --recipient=<admin-recipient> --recipient=<backup-recipient>
```

The command writes `secrets.age` and `secrets.pub.json`, the public half, which
[`chalkos.cluster.osCA`](../reference/options.md#chalkosclusterosca) names so the images trust
the OS CA. To decrypt the file, chalkctl tries `~/.config/chalkos/age.key`, `~/.ssh/id_ed25519`
and `~/.ssh/id_rsa`, or the files `--identity` names; a plugin identity file goes there too.
`--plaintext` writes `secrets.json` unencrypted, for a file protected by other means.

Keep the files in the flake's directory, where chalkctl looks for them by default.
`secrets.pub.json` must be tracked by git, because the flake reads it. Committing `secrets.age`
puts it under the same review and history as the definition. Every clone then carries it,
though, so its protection rests on the recipients' keys alone.

Two kinds of files appear next to it later. [`chalkctl rotate`](../reference/cli/chalkctl_rotate.md)
and [`chalkctl node-ca rotate`](../reference/cli/chalkctl_node-ca_rotate.md) rewrite the file in
place and keep the previous version as `secrets.age.prev`, encrypted like the original and
holding the old keys. Every command that changes the file holds `secrets.age.lock` while it runs.
Leave both out of version control. The file's recipients change only when one of those commands
rewrites it with `--recipient`.

Back the file up to a second place. Without it you cannot install or reinstall a node, renew a
node certificate by hand, issue client files or kubeconfigs, rotate anything or derive a
recovery key. [Recover a node](recover-node.md#the-secrets-file-is-lost) lists what still works.

## Give each person a client file

Day-to-day work does not need the secrets file. A [client file](../reference/glossary.md#client-file)
holds a certificate of one [client role](../reference/glossary.md#client-role) for one person:

```sh
chalkctl config new --name=alice --role=operator --out=alice.json
```

| Role | May |
| --- | --- |
| reader | read node information, disks, status, logs and etcd's members |
| operator | also reboot, upgrade, drain and uncordon nodes |
| admin | also bootstrap, reset volumes and change etcd's members |

A client file is valid for a year unless `--ttl` says otherwise, and nothing revokes a single
one: taking access away means [rotating the OS CA](rotate-certificates.md). Issue them per
person, with the shortest validity your routine tolerates, so a departure is a rotation you
plan rather than one you rush.

Kubeconfigs from [`chalkctl kubeconfig`](../reference/cli/chalkctl_kubeconfig.md) are the same
for the Kubernetes API: one per person with `--name`, all in the group bound to
`cluster-admin`, revoked only by rotating the Kubernetes CAs. For narrower access, configure an
authenticator of the API server through
[`chalkos.cluster.kubernetes.extraArgs.kube-apiserver`](../reference/options.md#chalkosclusterkubernetesextraargskube-apiserver).

## Choose time servers

Nodes check certificates against their clocks, so a node whose clock drifts refuses valid
certificates or accepts expired ones. chalkos runs chrony with
[`chalkos.time.servers`](../reference/options.md#chalkostimeservers), by default three servers
of the German national metrology institute (PTB), authenticated with NTS. NTS needs the nodes
to reach the servers on TCP port 4460 as well as UDP port 123.

A network without outbound NTP needs internal servers. Use NTS where your servers support it:

```nix title="cluster.nix"
{
  chalkos.time.servers = [
    { host = "ntp1.example.internal"; }
    { host = "ntp2.example.internal"; nts = false; }
  ];
}
```

[`chalkos.time.dhcpServers`](../reference/options.md#chalkostimedhcpservers) adds the servers
DHCP announces, which nothing authenticates.

## Plan registry mirrors

Nodes pull the control plane's images, flannel and CoreDNS from their upstream registries.
[`chalkos.cluster.registries.mirrors`](../reference/options.md#chalkosclusterregistriesmirrors)
puts HTTPS mirrors in front of a registry host; containerd tries them in order and falls back
to the registry. A cluster without internet access needs every image in a mirror, including
those [`chalkos.cluster.kubernetes.images`](../reference/options.md#chalkosclusterkubernetesimagesetcd)
names.

```nix title="cluster.nix"
{
  chalkos.cluster.registries.mirrors = {
    "registry.k8s.io" = [ "https://mirror.example.internal" ];
    "docker.io" = [ "https://mirror.example.internal" ];
  };
}
```

## Plan image versions and upgrades

Every role image carries a version, `system.image.version`, set in the role's NixOS modules. Any
change to an image, from a new chalkos input to a new Kubernetes release, needs a new version,
because chalkd refuses an image of the version it runs with another store. Settle on a scheme
before the first install, such as one version for every role raised together.

A routine that works: change the definition, upgrade a lab of the same roles with it, then run
`chalkctl upgrade` on the cluster. Kubernetes moves one minor release at a time, as upstream
requires. [Upgrade a cluster](upgrade-cluster.md) describes a run.

## Note when certificates expire

chalkos renews what it can by itself and warns in `chalkctl status` before anything else
expires.

| What | Lifetime | Renewed by |
| --- | --- | --- |
| OS CA | 10 years | [`chalkctl rotate os-ca`](rotate-certificates.md); status warns a year ahead |
| Node CA | 5 years | `chalkctl node-ca rotate`; status warns 18 months ahead |
| Kubernetes CAs | 10 years | `chalkctl rotate kubernetes-ca`; status warns a year ahead |
| Node certificates | 1 year | chalkd, after two thirds of the lifetime; nodes without Kubernetes need `chalkctl node renew` |
| Control-plane certificates | 1 year | chalkd, after two thirds of the lifetime |
| Kubelet certificates | 1 year | the kubelet |
| Client files and kubeconfigs | `--ttl`, 1 year by default | issuing new ones |

A node whose role has no Kubernetes has no control plane to renew its node certificate, so put
`chalkctl node renew` for those nodes in the yearly calendar.

## Plan backups

chalkos has no etcd backup or restore command yet. Three control planes protect against losing a
machine; they do not bring back an object someone deleted. Back up what your workloads store with
tools that work at the Kubernetes level, and back up these yourself:

- the secrets file and its backup recipient's key;
- the flake's repository, with `flake.lock`, which pins the chalkos version every image is built
  from;
- the Secure Boot db key and certificate;
- a password file for each node whose fallback is a password.

## Check that it worked

Evaluate the cluster's manifest. Evaluation stops on an invalid storage definition and prints
warnings such as the one for an unencrypted STATE:

```sh
nix eval --json .#chalkos.<cluster>.manifest > /dev/null
```

`<cluster>` is the cluster's name. Then build one role image, which checks what only the image
knows, such as its version:

```sh
nix build .#chalkos.<cluster>.roles.<role>.images.<platform>
```

## What next

- [Install on bare metal](install-bare-metal.md) or [run on KVM, Proxmox or libvirt](kvm-proxmox-libvirt.md).
- [Run a highly available control plane](ha-control-plane.md).
- [Security model](../concepts/security.md) and [Certificates](../concepts/certificates.md)
  explain what the keys protect.
- [Storage and encryption](../concepts/storage.md) explains the volumes and their keys.
