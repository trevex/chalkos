---
title: "Troubleshooting"
description: "Find out why a node or the cluster misbehaves, starting from chalkctl status"
---

# Troubleshooting

Most problems on a chalkos node show up in one of four places: the node's status, the journal of
one of its units, its console, or a shell inside it. This page explains how to read each of
them, then lists common symptoms with their cause and fix. When a symptom points at a node that
does not unlock, boot, renew or rejoin, [Recover a node](recover-node.md) has the fix.

## Before you begin

- A reader [client file](../reference/glossary.md#client-file) or the
  [secrets file](../reference/glossary.md#secrets-file), for `chalkctl status` and
  `chalkctl logs`.
- The node's console, through its BMC, in person, or with `chalklab console` in a
  [lab](../reference/glossary.md#lab).
- A kubeconfig, for the shell inside a node.

## Read the node's status

[`chalkctl status`](../reference/cli/chalkctl_status.md) is the first command to run. It asks
the node's [chalkd](../reference/glossary.md#chalkd) for everything chalkd checks, and compares the node's identity with the
[cluster definition](../reference/glossary.md#cluster-definition). A healthy [worker](../reference/glossary.md#worker):

```console
$ chalkctl status w1
identity b02445f30a5a8197dac34ce49e8af8c62f9974d2c7b8dbf02ffb1d54cd620a65 (the cluster definition's)
platform metal (the cluster definition's)
image 1.5.0, booted from chalkos_1.5.0.efi
VOLUME    DISK      MOUNT POINT        STATE
longhorn  longhorn  /var/lib/longhorn  mounted
var       system    /var               mounted
kubernetes worker: joined, node ready: True
certificates:
  node             expires 2027-10-10
  OS CA            expires 2035-09-15
  Kubernetes CA    expires 2035-09-15
  kubelet client   expires 2027-08-21
  kubelet serving  expires 2027-08-21
trust:
  OS CA          e7b462f19ed536c1
  Kubernetes CA  980328b6f68680b0
time: synchronised to 192.53.103.108, offset +0.000214 s
```

Read it from the top:

| Line | Says | When it is wrong |
| --- | --- | --- |
| `identity` | The version of the [identity](../reference/glossary.md#identity) the node runs, and whether it is the cluster definition's. | `not the cluster definition's; chalkctl apply-identity w1 delivers it`: the definition changed since the last delivery. |
| `platform` | The platform the image was built for. | A mismatch with the definition: changing a node's platform is a reinstall. |
| `image` | The [image version](../reference/glossary.md#image-version) booted, its boot entry, and `not found healthy yet` while the [health check](../reference/glossary.md#health-check) runs. | `upgrade to ... failed: rolled back to ...`, followed by the failed boot's log lines: see [Upgrade a cluster](upgrade-cluster.md#if-a-node-rolls-back). |
| `VOLUME` table | Each volume, its disk, mount point and state. | `missing` or `not mounted`; a `disk <name>: ...` line after the table names the disk's problem. |
| `kubernetes` | The node's kind and state, whether its Node is Ready, its part in the [VIP](../reference/glossary.md#vip) and its [control plane](../reference/glossary.md#control-plane)'s state. | Any state other than `bootstrapped` or `joined`, or one of them followed by `: <problem>`; the table of symptoms below lists them. |
| `certificates:` | Every certificate the node holds or issues, with its expiry. | A third column names what needs doing. |
| `trust:` | The CAs and keys the node trusts, by fingerprint; `(issues)` marks the one that issues. | Two fingerprints outside a [rotation](../reference/glossary.md#rotation). |
| `time` | Whether chrony synchronised the clock, to which source and with what offset. | `not synchronised`. |
| `failed unit` | Each systemd unit that failed in this boot. | Any line: read that unit's log. |

A control plane's Kubernetes line adds `vip holder` or `vip standby` when
[`vip.addresses`](../reference/options.md#chalkosclusterkubernetesvipaddresses) is set, and the
control plane's state, `current` when its API server and etcd serve the node's certificates and the
API server answers ready.

## Read a unit's log

[`chalkctl logs`](../reference/cli/chalkctl_logs.md) prints a node's journal of the current
boot, of every unit or of `--unit` alone; `-f` follows it:

```sh
chalkctl logs <node> --unit=<unit> -f
```

The units that matter most:

| Unit | Does |
| --- | --- |
| `chalkd.service` | The node agent: installs, upgrades, identity, certificates, joining etcd, the VIP. |
| `chalkos-identity.service` | Applies the node's identity at boot: hostname, network, time servers. |
| `chalkos-kubernetes.service` | Picks the node's addresses and writes the Kubernetes certificates and configuration before the kubelet starts. |
| `chalkos-health.service` | Decides whether a boot after an upgrade is healthy. |
| `kubelet.service` | The kubelet, which runs the static pods of etcd and the control plane, and every pod. |
| `containerd.service` | The container runtime: image pulls and mirrors. |

`chalkctl logs` shows the current boot only. For a boot that fell back after an upgrade, the
health check keeps the last 30 lines that chalkd and the check logged on [VAR](../reference/glossary.md#var), and
`chalkctl status` shows them.

## Watch the console

The console shows what happens before chalkd serves: the boot loader, the initrd, a prompt for a
disk's passphrase, and chalkd's own output, which includes the certificate fingerprint and the
node's addresses in [maintenance mode](../reference/glossary.md#maintenance-mode). On hardware
the console is the BMC's serial-over-LAN or a screen. In a lab,
[`chalklab console`](../reference/cli/chalklab_console.md) prints a VM's serial console and
follows it; it is read-only, so a lab node cannot answer a passphrase prompt.

## Open a shell on a node

Nodes have no logins and no SSH. With
[`chalkos.debug.tools`](../reference/options.md#chalkosdebugtools) in a role's NixOS modules, the
role image carries coreutils, grep, sed, findutils, procps, iproute2, util-linux and, on
Kubernetes roles, crictl; systemd, bash, less, nftables and kmod are always there. A privileged
pod on the node then reaches them through the host's root:

```sh
kubectl debug node/<node> -it --profile=sysadmin --image=busybox -- \
  chroot /host /run/current-system/sw/bin/bash
```

The option changes the image, so it reaches running nodes with an upgrade and a new version. It
needs the node's kubelet and the API server to work; a node that is not in Kubernetes has only
`chalkctl logs` and its console.

## Common problems

Each problem starts from what the status, the console or kubectl shows.

### The node is in maintenance mode after the install

The console shows a fingerprint again, and `chalkctl status` cannot verify the node. The
machine booted the [installer](../reference/glossary.md#installer) medium again, or the install did not finish.
[Recover a node](recover-node.md#the-node-is-in-maintenance-mode) has both fixes.

### The console asks for a passphrase

The [TPM](../reference/glossary.md#tpm) did not unseal [STATE](../reference/glossary.md#state), usually after a change to [Secure Boot](../reference/glossary.md#secure-boot) or the firmware. Type the
node's [recovery key](../reference/glossary.md#recovery-key); [Recover a node](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
explains the causes and what comes after.

### The node has no address

The Kubernetes line says `preparation failed: no ipv4 node address matches ...`, with the
selector and the addresses the node has. The node waited
[`nodeIP.timeout`](../reference/options.md#chalkosclusterkubernetesnodeiptimeout) seconds, 300
by default, for an address that matches its fixed `nodeIPs` or its `validSubnets`, and found
none, so it runs neither the kubelet nor static pods. Check the node's network definition and
DHCP, and that the subnets list the network the node is on. Then deliver a corrected definition
with `chalkctl apply-identity <node>`, or reboot once the network is right.

A control plane that lost an address it was pinned to says `pinned address ... is not present`
instead; see [Recover a node](recover-node.md#a-control-plane-cannot-rejoin-etcd).

### A control plane waits for bootstrap

`waiting for bootstrap or for the cluster at https://...` is the state of every control plane
before the cluster exists: run `chalkctl bootstrap` on exactly one. After the bootstrap it means
the node finds no API server of its cluster at the endpoint. Check that the endpoint resolves,
that the VIP is held (another control plane's status shows `vip holder`), and that the node
reaches port 6443 there.

### A control plane does not finish joining

The status names the join's step or its last error after `joining the cluster at ...:`. A node
that stays at `etcd member ... catches up` cannot reach the other members on port 2380, or its
etcd is still copying a large database. `etcd has a member <node> already` is a member left over
from an earlier install: remove it, as
[Recover a node](recover-node.md#a-control-plane-cannot-rejoin-etcd) describes.

### kubectl logs and exec fail on a node

The API server reaches kubelets with their serving certificates, which the kubelet requests
through a CertificateSigningRequest and chalkd on a control plane approves. chalkd approves a
request only when the Node lists every address and name it asks for, and leaves the others
pending:

```sh
kubectl get csr
```

A pending `kubernetes.io/kubelet-serving` request usually asks for an address the Node does not
report. Check the node's addresses in `kubectl get node <node> -o wide` against its network
definition.

### Image pulls fail

Pods stay in `ErrImagePull` or `ImagePullBackOff`. containerd pulls through the mirrors of
[`chalkos.cluster.registries.mirrors`](../reference/options.md#chalkosclusterregistriesmirrors)
and then the registry itself. Read `containerd.service`'s log on the node for the URL that
failed. Mirrors must be `https://` unless
[`allowPlainHTTP`](../reference/options.md#chalkosclusterregistriesallowplainhttp) is set, and a
cluster without internet access needs every image in a mirror.

### Pods on different nodes cannot reach each other

Pod traffic between nodes travels in flannel's VXLAN, on UDP port 8472.

- A node whose address lies outside
  [`vxlanSourceSubnets`](../reference/options.md#chalkosclusterkubernetesvxlansourcesubnets)
  refuses to prepare and says `the node's address ... is outside vxlanSourceSubnets`; the other
  nodes would drop its VXLAN. Widen the ranges, which changes every image.
- Small packets pass and large ones hang, as with TLS handshakes or large responses: the VXLAN
  MTU is too big for the network. Set
  [`chalkos.cni.flannel.mtu`](../reference/options.md#chalkoscniflannelmtu) to the network's MTU,
  minus 20 when IPv6 is in use.
- A firewall between the nodes drops UDP 8472.

### A Node has no pod range

A worker's state ends in `the Node has no ipv6 pod range; the control plane allocates ranges of
the families the cluster was created with`. The families in
[`ipFamilies`](../reference/options.md#chalkosclusterkubernetesipfamilies) changed after the
cluster was created, which is not supported. Restore the original families.

### An upgrade rolled back or stopped

`chalkctl upgrade` stops at a node whose new image did not become healthy, at a control plane
whose reboot would cost etcd its quorum, or at a drain that does not finish.
[Upgrade a cluster](upgrade-cluster.md#if-something-goes-wrong) lists the messages and fixes.

### Status warns about a certificate

| Third column | Means | Do |
| --- | --- | --- |
| `less than a third of its lifetime remains` | The [node certificate](../reference/glossary.md#node-certificate) is due for renewal. | Nothing on a node with Kubernetes, which renews it; `chalkctl node renew <node>` on one without. |
| `renewal failing: ...; expires ...` | The node could not renew through a control plane. | Fix what the message names, usually the endpoint not reaching chalkd on port 50000. |
| `expired` | The certificate expired. | [Recover a node](recover-node.md#the-node-certificate-expired). |
| `node CA expires ...; run chalkctl node-ca rotate` | The [node CA](../reference/glossary.md#node-ca) has less than 18 months left. | `chalkctl node-ca rotate`. |
| `OS CA expires ...`, `Kubernetes CA expires ...` | A CA has less than a year left. | [Rotate it](rotate-certificates.md). |
| `less than a tenth of its lifetime remains, though the kubelet renews it itself` | The kubelet did not renew its certificate. | Read `kubelet.service`'s log; look for pending CertificateSigningRequests. |

### The clock is not synchronised

`time: not synchronised; certificates are checked against this clock`. chrony reached none of
its servers. With the default servers, NTS needs the node to reach them on TCP port 4460 and UDP
port 123. A network that blocks both needs internal servers in
[`chalkos.time.servers`](../reference/options.md#chalkostimeservers). Certificates start an hour
before they are issued, so a small offset does no harm, but a clock that is days off rejects
valid certificates.

### The lab does not start

`chalklab create` and `chalklab start` run each node as a QEMU virtual machine on KVM.

- `/dev/kvm does not exist`: the machine has no hardware virtualisation, it is disabled in the
  firmware, or the kvm module is not loaded. [chalklab](../reference/glossary.md#chalklab) needs an x86-64 machine with KVM.
- `you may not open /dev/kvm`: join the `kvm` group (on NixOS, add it to
  `users.users.<you>.extraGroups`) and log in again.
- `<node> did not come up in maintenance mode`: the VM booted but chalkd did not print its
  fingerprint. The error names the console log; `chalklab console <node>` shows it.
- `the lab of <cluster> exists in ...; chalklab destroy removes it`: a lab of this cluster
  exists. `chalklab start` starts it again; `chalklab destroy` removes it.
- The VMs start but are slow, or one stops: the lab needs about 3 GiB of memory for a control
  plane and 2 GiB for each other node. `chalklab status` shows which VMs run.

## What to put in a bug report

A report someone can act on holds:

- the chalkos version, from `flake.lock`, and the image version from `chalkctl status`;
- the full output of `chalkctl status <node>` for the nodes involved;
- the logs of the units involved, from `chalkctl logs <node> --unit=<unit>`;
- for a boot problem, the console output, from the BMC or `chalklab console <node> -f=false`;
- the parts of the cluster definition that concern the problem, without secrets;
- the commands you ran and their full output.

Leave out the secrets file, client files, kubeconfigs and recovery keys: each grants access to
the cluster.

## What next

- [Recover a node](recover-node.md) fixes nodes that do not unlock, boot, renew or rejoin.
- [Boot, health and rollback](../concepts/boot-and-rollback.md) explains what the health check
  looks at.
- [Networking](../concepts/networking.md) explains address selection, the VIP and the pod
  network.
- [`chalkctl status`](../reference/cli/chalkctl_status.md) and
  [`chalkctl logs`](../reference/cli/chalkctl_logs.md) list their flags.
