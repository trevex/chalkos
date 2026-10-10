---
title: "Run a highly available control plane"
description: "Run three control planes behind a VIP, and add, replace or remove one"
---

# Run a highly available control plane

Three [control planes](../reference/glossary.md#control-plane) keep the Kubernetes API and etcd
available while one of them reboots or fails. This guide defines three control planes behind a
[VIP](../reference/glossary.md#vip), installs them, bootstraps one and lets the other two join on
their own. It then covers the changes a running control plane needs over its life: adding a
member, removing one and replacing a failed machine.

## Before you begin

- A [cluster definition](../reference/glossary.md#cluster-definition) and its [secrets file](../reference/glossary.md#secrets-file), as in
  [Plan a production cluster](production-cluster.md).
- Three machines on one layer-2 segment, each with a static address, and one free address on
  that segment for the VIP. The VIP is announced with gratuitous ARP and unsolicited neighbour
  advertisements, so it cannot cross a router.
- Each machine booted into the [installer](../reference/glossary.md#installer), or into its role
  image, in [maintenance mode](../reference/glossary.md#maintenance-mode), as
  [Install on bare metal](install-bare-metal.md) describes.

## Define three control planes and the VIP

Put the VIP in [`chalkos.cluster.kubernetes.vip.addresses`](../reference/options.md#chalkosclusterkubernetesvipaddresses)
and make the [cluster endpoint](../reference/glossary.md#cluster-endpoint) point at it. Every node and client reaches the API server at the
endpoint, so the endpoint has to follow the VIP from one control plane to the next.

```nix title="cluster.nix"
{
  chalkos.cluster = {
    name = "prod";
    endpoint = "https://10.0.0.10:6443";
    osCA = ./secrets.pub.json;
    kubernetes.vip.addresses = [ "10.0.0.10" ];
  };

  chalkos.roles.controlplane.kubernetes.kind = "controlplane";

  chalkos.nodes = {
    cp1 = {
      role = "controlplane";
      storage.system.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001";
      network.networks."10-uplink" = {
        matchConfig.Name = "enp1s0";
        address = [ "10.0.0.11/24" ];
        gateway = [ "10.0.0.1" ];
      };
    };
    # cp2 and cp3 alike, at 10.0.0.12 and 10.0.0.13.
  };
}
```

A dual-stack cluster takes one VIP per family. [chalkd](../reference/glossary.md#chalkd) adds the VIP to the interface that holds
the node's address of the VIP's family, unless
[`vip.interface`](../reference/options.md#chalkosclusterkubernetesvipinterface) names another.

## Install every control plane

Install the three nodes as any other node, each with the fingerprint its console shows in
maintenance mode:

```sh
chalkctl install cp1 --fingerprint=<fingerprint>
chalkctl install cp2 --fingerprint=<fingerprint>
chalkctl install cp3 --fingerprint=<fingerprint>
```

Each node reboots into its installed image. Until one of them is bootstrapped, none can tell
whether it is meant to start the cluster or join it, so all three wait.
[`chalkctl status`](../reference/cli/chalkctl_status.md) shows it in the Kubernetes line:

```text
kubernetes controlplane: waiting for bootstrap or for the cluster at https://10.0.0.10:6443
```

## Bootstrap exactly one

[`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md) starts etcd on one node as the
cluster's first member, starts the control-plane components and applies the cluster's
manifests:

```console
$ chalkctl bootstrap cp1
bootstrapping cp1; the control plane pulls its images and starts
cp1 is bootstrapped; applied 19 objects
```

Bootstrap only this one node. A second bootstrap would start a second etcd cluster with its own
data, so chalkd refuses it in every case it can detect: on a node that is bootstrapped already
(`the node is bootstrapped already`), on a node that holds etcd data, and on a node whose cluster
answers at the endpoint (`the cluster's API server answers at https://10.0.0.10:6443; this node
joins it on its own`).

Once cp1's API server is ready, cp1 wins the VIP's election and holds the VIP.

## Watch the others join

cp2 and cp3 find the cluster at the endpoint and join etcd on their own, one after the other:
etcd accepts one learner at a time. Each node adds itself as a learner, starts its [etcd member](../reference/glossary.md#etcd-member),
waits until the member caught up with the leader, has it promoted to a voter and then starts the
rest of the control plane. Its status names the step:

```text
kubernetes controlplane: joining the cluster at https://10.0.0.10:6443: checking etcd's members
kubernetes controlplane: joining the cluster at https://10.0.0.10:6443: etcd member 5b1b8a3c0e9a71f2 catches up
```

Before the node adds itself as a learner, it pins its addresses on
[STATE](../reference/glossary.md#state): from then on, every boot waits for exactly these
addresses before it starts etcd. A joined control plane reports:

```text
kubernetes controlplane: bootstrapped, node ready: True, vip standby, control plane current
```

`vip standby` means the node takes part in the VIP's election; the holder shows `vip holder`.
`control plane current` means its API server and etcd serve the certificates the node holds and
the API server answers ready.

## Check that it worked

[`chalkctl etcd members`](../reference/cli/chalkctl_etcd_members.md) lists etcd's members as
one control plane's member sees them. Three healthy voters is the goal:

```console
$ chalkctl etcd members
NAME  ID                PEER URLS               MEMBER  HEALTH
cp1   8e9e05c52164694d  https://10.0.0.11:2380  voter   healthy
cp2   5b1b8a3c0e9a71f2  https://10.0.0.12:2380  voter   healthy
cp3   d2f4c87a1b3e5096  https://10.0.0.13:2380  voter   healthy
```

`--via=<node>` asks a particular control plane, which helps when two of them disagree. Then
check that the API server answers through the VIP:

```sh
chalkctl kubeconfig --out=prod.kubeconfig
kubectl --kubeconfig=prod.kubeconfig get nodes
```

## Expect a VIP failover to take seconds

When the holder's API server stops answering, or the holder fails, another healthy control plane
takes the VIP through an election in etcd; [Networking](../concepts/networking.md#how-does-the-vip-move-between-control-planes)
explains the timing. In the HA tests the VIP moved within 30 seconds of its holder's network link
going down, and it stayed with the new holder when the old one came back. Clients with an open
connection to the old holder can take longer to notice: in one upgrade test a kubelet went more
than 40 seconds without renewing its lease. A cluster without etcd quorum has no holder, because
nobody can win the election.

## Add a control plane

Define the new node with the control-plane role and install it. It joins as cp2 and cp3 did,
with no further command. Grow to five, not four: four voters need three for a quorum and so
tolerate one failure, like three.

## Remove a control plane

Take the node out of etcd before you switch it off.
[`chalkctl etcd leave`](../reference/cli/chalkctl_etcd_leave.md) makes the node release the VIP,
remove its own member, stop its static pods, delete its etcd data and unpin its addresses:

```console
$ chalkctl etcd leave cp3
cp3 left etcd; reinstall it to join the cluster again
```

The removal is refused when the voters left would have fewer healthy members than their quorum
needs, because etcd would then stop accepting writes. The message names the counts:
`without cp3 etcd has 2 voters of which 1 are healthy, fewer than the 2 a quorum needs`. Bring
the unhealthy members back first.

The node now reports `left etcd; reinstall the node to join the cluster again` and does not
join again on its own, also after a reboot. Delete its Node with `kubectl delete node cp3`, power
it off and remove it from the cluster definition.

## Replace a failed machine

A control plane whose machine is gone cannot leave by itself. Remove its member through the
others with [`chalkctl etcd remove-member`](../reference/cli/chalkctl_etcd_remove-member.md),
which takes the node's name or the member's ID:

```console
$ chalkctl etcd remove-member cp3
removed the etcd member cp3 (d2f4c87a1b3e5096)
```

The same quorum guard applies. `--force` skips it, and etcd then accepts no writes until enough
of the remaining members are healthy again.

Then install the replacement machine under the same name. Give it the addresses the definition
declares, boot it into the installer, and install it; `--wipe-disk` lets the installer replace
whatever its disk holds:

```sh
chalkctl install cp3 --fingerprint=<fingerprint> --wipe-disk
```

It joins as a new member.

## Reinstall a control plane

A control plane that should keep its machine but start over, for a new disk layout or a lost
STATE, leaves first and is reinstalled after:

```sh
chalkctl etcd leave cp3
chalkctl install cp3 --fingerprint=<fingerprint> --wipe-disk
```

Between the two commands, boot the machine into the installer, through its BMC or boot menu: an
installed node runs no maintenance mode, so `chalkctl install` cannot reach it.

## If something goes wrong

These failures are specific to control planes.

### A reinstalled node finds its old member

A control plane reinstalled without `chalkctl etcd leave` finds a member of its own name in etcd
and stops, because joining would hand it the old member's identity without its data:

```text
kubernetes controlplane: joining the cluster at https://10.0.0.10:6443: etcd has a member cp3 already; remove it with chalkctl etcd remove-member cp3
```

Run the command the status names. The node tries again every ten seconds and joins on its next
attempt.

### A control plane lost its pinned address

A pinned control plane that boots without one of its addresses, after a cabling or switch
change for example, waits for it for
[`nodeIP.timeout`](../reference/options.md#chalkosclusterkubernetesnodeiptimeout) seconds and then
runs neither etcd nor the kubelet:

```text
kubernetes controlplane: preparation failed: pinned address 10.0.0.13 is not present, 5m0s after chalkos-node-addresses.target; restore it, or remove the node's etcd member with chalkctl etcd remove-member cp3 and reinstall the node
```

The other two keep the cluster running. Restore the address and reboot the node, or take it out:
its own etcd member does not answer, so `chalkctl etcd leave cp3` refuses unless given `--force`,
with which it removes the member through the others and cleans up the node. Then reinstall it.

### A node that left wants back in

A node that left etcd stays out until it is reinstalled; its status says so. Reinstall it as
in [Reinstall a control plane](#reinstall-a-control-plane).

[Recover a node](recover-node.md) covers nodes that do not unlock or boot, and
[Troubleshooting](troubleshooting.md) starts from symptoms.

## What next

- [Kubernetes on chalkos](../concepts/kubernetes.md) explains bootstrap, join and the static
  pods.
- [Networking](../concepts/networking.md) shows the HA network layout and how nodes pick their
  addresses.
- [Upgrade a cluster](upgrade-cluster.md) rolls a new image through the control planes one at a
  time.
- [`chalkctl etcd`](../reference/cli/chalkctl_etcd.md) lists the etcd commands and their flags.
