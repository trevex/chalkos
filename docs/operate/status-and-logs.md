---
title: "Status, logs and the console"
description: "Read what chalkctl status reports, follow a unit's journal, run the image's diagnostics, watch the console and reach a node's tools"
---

# Status, logs and the console

This page is for an operator who wants to know what a node is doing. It explains each line of
`chalkctl status`, then the four places that tell more: a unit's journal, the image's
diagnostics, the console and a shell on the node. The examples read the homelab's worker `w1`.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes.
`chalkctl status`, `chalkctl logs` and `chalkctl node diag` need a reader client file or the
secrets file. The diagnostics that send traffic from the node or show its firewall need an
operator's. The console needs the machine's BMC, a screen, or `chalklab console` in a lab.

## chalkctl status reads the node from the top

`chalkctl status` asks the node's chalkd for what it checks, and compares the node's config and
image with the inventory. A healthy `w1` reads:

```console
$ chalkctl status w1 --secrets secrets.age
config <version> (the inventory's)
platform metal (the inventory's)
image <image>, booted from chalkos_1.efi
VOLUME    DISK      MOUNT POINT        STATE
longhorn  longhorn  /var/lib/longhorn  mounted
var       system    /var               mounted
OUTPUT     HASH    STATE
bird-conf  <hash>  rendered
chrony     <hash>  rendered
hardware   <hash>  rendered
networkd   <hash>  rendered
kubernetes worker: joined, node ready: True
certificates:
  node             expires <date>
  OS CA            expires <date>
  Kubernetes CA    expires <date>
  kubelet client   expires <date>
  kubelet serving  expires <date>
trust:
  OS CA          <fingerprint>
  Kubernetes CA  <fingerprint>
time: synchronised to <server>, offset <offset> s
```

The placeholders stand for values that differ on every node:

- `<version>` is the config's version, the SHA-256 of its documents, in 64 hex characters.
- `<image>` is the image's name: the first 12 hex characters of its root hash, followed by the
  tag and revision it was built from when the node knows them.
- `<hash>` is the first 12 hex characters of the hash of an output's files.
- `<date>` is an expiry date, such as `2027-10-10`.
- `<fingerprint>` is the first 16 hex characters of a CA's fingerprint.
- `<server>` and `<offset>` are chrony's time source and the clock's offset from it, in seconds
  with a sign.

The sections below explain the lines in this order. A section that has nothing to show leaves
its lines out.

## The config line compares the node with the inventory

`config <version> (the inventory's)` means the node runs the config the inventory gives it. The
parentheses say otherwise when they differ:

- `not the inventory's; chalkctl apply-config w1 delivers it`: the node's files changed since the
  last delivery.
- `cannot read the config the inventory gives it: ...`: chalkctl could not build the node's
  documents from the inventory, for the reason it names.
- `no inventory to compare it with`: chalkctl found no inventory, neither by `--inventory` nor as
  `chalkos.yaml` in the current directory.

`config none` is a node installed before config documents existed. A node with a staged image adds
`staged config <version> (...)`, the config that image boots with. Its parentheses name
`chalkctl apply-config --staged w1` when it differs.

## The platform and image lines name what the node booted

`platform metal (the inventory's)` names the platform the node's image was built for. A node
whose image in the inventory is built for another platform shows
`platform kvm, but ...; changing a node's platform is a reinstall`.

The image line names the image the node booted and its boot entry. A boot that the health check
still checks ends in `, not found healthy yet`. More lines follow after an upgrade:

- `upgrade to <image> installed; it boots next`: an upgrade with `--no-reboot` staged the image.
- `upgrade to <image> failed: rolled back to <image>`: the new image was not found healthy, and
  the node booted its previous one. The last 30 lines that chalkd and the health check logged in
  the failed boot follow, indented.
  [A node that fails its health check rolls back](upgrade.md#a-node-that-fails-its-health-check-rolls-back)
  says what to do.
- `its store's root hash is ..., not that of the image ... the inventory names, ...`: the image was
  rebuilt since the node's upgrade. The line ends in `chalkctl upgrade --nodes w1 installs that
  image`, or in `it has ... staged, which it boots with its next reboot` when it waits for a
  reboot.

`image ...; its boot is unknown: ...` means chalkd could not read the boot entries on the ESP or
the running store, for the reason named.

## The volume table lists the node's storage

Each row names a volume, the disk it lives on, its mount point and its state:

- `mounted`: the volume is mounted.
- `present`: the volume exists and has no mount point, as a raw volume does.
- `not mounted`: the volume exists, but its mount failed or its disk is gone.
- `missing`: the node never created the volume's partition.

A line `disk <name>: <problem>` after the table names a disk chalkd could not use, such as
`disk longhorn: pinned disk ... is missing`.
[Add storage volumes](storage-volumes.md#if-something-goes-wrong) covers those problems.

## The output table shows each rendered output

An output is a set of files that the image renders from the config, such as chrony's sources.
Each row names an output, the hash of the files the node serves, and its state:

- `rendered`: the last render and its check passed.
- `failed: <error>`: the last render or its check failed, for the reason named.
- `not rendered yet`: no render ran yet. The hash column shows `none`.

The image author declares the outputs, as
[Render config into files](../build/render-config.md) describes.

## The Kubernetes line shows the node's part in the cluster

On the shipped variant, `kubernetes worker: joined, node ready: True` names the node's kind, its
state and whether its Node is Ready. A control plane adds its VIP role, `vip holder` or
`vip standby`, and `control plane current` when its API server and etcd serve the current
certificates and the API server answers ready.
[Kubernetes status states](../reference/kubernetes.md#kubernetes-status-states)
lists every state, and [Troubleshoot Kubernetes](../kubernetes/troubleshooting.md) acts on them.

## The certificates section warns before an expiry

Each line names a certificate the node holds or issues and its expiry. A third column names what
needs doing:

- `less than a third of its lifetime remains`: the node certificate is due for renewal. A node of
  a Kubernetes cluster renews it through a control plane. Any other node adds
  `; renew it with chalkctl node renew w1`.
- `renewal failing: <reason>; expires <time>`: the node could not renew through a control plane.
  Fix what the reason names, often a cluster endpoint that does not reach chalkd on port 50000.
- `expired`: the certificate expired.
  [The node certificate expired](recover-node.md#the-node-certificate-expired) brings the node
  back.
- `OS CA expires <date>`: the OS CA has less than a year left.
  [Rotate the OS CA](access.md#rotate-the-os-ca) replaces it.
- `node CA expires <date>; run chalkctl node-ca rotate`: the node CA has less than 18 months left.
  [Rotate the node CA before it expires](access.md#rotate-the-node-ca-before-it-expires) covers
  it.

`expiry unknown` in the second column means the node could not read the certificate's expiry.

On the shipped variant the Kubernetes certificates follow: its CAs, a control plane's own
certificates and the kubelet's. `Kubernetes CA expires <date>` warns a year ahead.
`less than a tenth of its lifetime remains, though the kubelet renews it itself` means the kubelet
did not renew its certificate. [Troubleshoot Kubernetes](../kubernetes/troubleshooting.md) and
[Rotate the Kubernetes CAs](../kubernetes/rotate-kubernetes-cas.md) cover these lines.

## The trust section lists the CAs by fingerprint

Each line names a CA or key set the node trusts, by the first 16 hex characters of each
fingerprint. `(issues)` marks the one that signs new certificates. Two fingerprints on one line
are normal during a rotation and wrong outside one.
[Manage access and certificates](access.md#a-rotation-runs-in-four-phases-with-pauses) explains the
phases.

## The time line shows the clock

`time: synchronised to <server>, offset <offset> s` means chrony synchronised the clock. A node
whose clock is not synchronised shows
`time: not synchronised; certificates are checked against this clock`. chrony reached none of its
servers. `time: unknown: ...` means chalkd could not ask chrony.

Certificates start an hour before they are issued, so a small offset does no harm. A clock that is
days off rejects valid certificates. With the default servers, NTS needs the node to reach them on
TCP port 4460 and UDP port 123. A network that blocks both needs its own servers, as
[TimeSync names the time servers](node-config.md#timesync-names-the-time-servers) shows.

## A failed unit line names a unit to read

`failed unit <unit>` names each systemd unit that failed in this boot. A healthy node has none.
Read the unit's journal next.

## chalkctl logs prints a unit's journal

`chalkctl logs` prints the node's journal of the current boot, of every unit or of `--unit`
alone. `-f` follows it:

```sh
chalkctl logs w1 --unit=chalkd.service -f
```

These units cover most of a node's own work:

| Unit | Does |
| --- | --- |
| `chalkd.service` | The node agent: installs, upgrades, config, certificates and storage. |
| `chalkos-config.service` | Applies the node's config at boot: hostname, network, time servers and outputs. |
| `chalkos-health.service` | Decides whether a boot after an upgrade is healthy. |
| `chalkos-chrony-dhcp.service` | Passes time servers that DHCP offers to chrony. |

The shipped variant adds `chalkos-kubernetes.service`, which prepares the node's addresses and
Kubernetes files before the kubelet starts, and `kubelet.service` and `containerd.service`.
`chalkos-registry-credentials.service` and `chalkos-registry-mirrors.service` write the
credentials and mirrors for the pods' image pulls.

`chalkctl logs` shows the current boot only. For a boot that fell back after an upgrade,
`chalkctl status` shows the lines the failed boot logged.

A node in maintenance mode answers `chalkctl logs` too, with `--fingerprint` or `--insecure`:

```sh
chalkctl logs w1 --insecure --unit=chalkd.service
```

## chalkctl node diag runs the image's diagnostics

A diagnostic is a read-only command that the node's image declares, such as `ip route get`.
chalkd runs it on the node and sends back what it prints, so a node without a login still shows
its addresses, routes and firewall. Without a diagnostic, `chalkctl node diag` lists them:

```console
$ chalkctl node diag w1 --secrets secrets.age
DIAGNOSTIC            ROLE      DESCRIPTION
addresses             reader    The addresses of the node's interfaces, as ip -json address show prints them.
bird-protocols        reader    The protocols bird runs and the state of their BGP sessions.
  [peer=<pattern>]              Name of a Peer document, whose session alone is shown; every protocol when left out. Matches ^[a-z0-9][a-z0-9.-]*$.
dns-query             operator  Looks up a name in DNS, through the node's resolver or the server given.
  name=<hostname>               Name to look up.
  [type=<enum>]                 Record type; A when left out. One of A, AAAA, CAA, CNAME, DS, DNSKEY, MX, NS, PTR, SOA, SRV, TXT.
  [server=<address>]            Address of the DNS server to ask; the node's resolver when left out.
links                 reader    The node's network interfaces and their state, as ip -json link show prints them.
neighbours            reader    The neighbour table of IPv4 ARP and IPv6 neighbour discovery, as ip -json neighbour show prints it.
nft-ruleset           operator  The node's nftables ruleset, as nft list ruleset prints it.
ping                  operator  Sends ICMP echo requests to an address and prints the replies, for at most 10 seconds.
  address=<address>             IPv4 or IPv6 address to ping.
  [count=<1-10>]                Number of echo requests. Default: 4.
route-get             reader    The route the kernel takes to an address.
  address=<address>             IPv4 or IPv6 address, without a zone: ip route get takes none, and finds a link-local address's interface by the routes.
routes                reader    The routes of one family in one routing table, as ip -json route show prints them.
  [family=<enum>]               Address family: inet for IPv4, inet6 for IPv6. One of inet, inet6. Default: inet.
  [table=<pattern>]             Routing table: main, local, default, all or a table's number. Matches ^(main|local|default|all|[0-9]+)$. Default: main.
tcp-connect           operator  Opens a TCP connection to an address and port, closes it again and says whether it connected.
  address=<address>             IPv4 or IPv6 address.
  port=<1-65535>                TCP port.
```

Each diagnostic names the least role that runs it. Readers run those that read the node's state.
`ping`, `dns-query` and `tcp-connect` send traffic from the node, and `nft-ruleset` shows its
firewall, so they need an operator. A parameter in brackets may be left out. `bird-protocols`
comes from the homelab's BGP extension. Every image has the others.

Name the diagnostic and give each parameter as `<parameter>=<value>`:

```sh
chalkctl node diag w1 route-get address=2001:db8::1
chalkctl node diag w1 routes family=inet6
chalkctl node diag w1 tcp-connect address=192.0.2.10 port=443
```

The command's output goes to standard output and its errors to standard error. chalkctl fails
when the command fails. A `tcp-connect` to a port that nothing listens on prints:

```text
tcp-connect: connect: Connection refused
tcp-connect: line 1: /dev/tcp/192.0.2.10/443: Connection refused
chalkctl: tcp-connect exited with status 1
```

A connect that gets no answer within 5 seconds prints
`no connection to 192.0.2.10 port 443 within 5 seconds` and exits with status 124.

chalkd checks each value against its parameter's type before it runs anything. It also refuses
any value that starts with `-` or holds a control character. A misspelt address fails with
`invalid_argument: diagnostic route-get: address: "2001:db8::g" is no IPv4 or IPv6 address`. A
`hostname` reaches the command fully qualified: `dns-query name=registry` asks for `registry.`,
and the node's DNS search domains do not apply. A command that runs longer than its timeout
fails with `route-get ran longer than its timeout, and the node stopped it`. chalkd sends at most
1 MiB of output, and a node runs four diagnostics at a time.

A node in maintenance mode answers `chalkctl node diag` too, with `--fingerprint` or
`--insecure`. The installer then shows whether the machine reaches a registry before it installs:

```sh
chalkctl node diag w1 --insecure tcp-connect address=2001:db8::5000 port=443
```

An image built without an OS CA accepts any client in maintenance mode and treats it as an admin.
A caller without a certificate may then run every diagnostic, `nft-ruleset` and the other
operator diagnostics included. It may call Install and Reboot as well. An image with the OS CA
takes only the client files of its cluster in maintenance mode, each with its own role.

## The console shows the boot before chalkd serves

The console shows the boot loader, the initrd, a passphrase prompt for an encrypted disk, and
chalkd's output. In maintenance mode, chalkd prints its certificate's fingerprint and the node's
addresses there. On hardware the console is the BMC's serial-over-LAN or a screen. In a lab,
`chalklab console` prints a VM's serial console and follows it:

```sh
chalklab console w1
```

The lab's console is read-only, so a lab node cannot answer a passphrase prompt.

## Debug tools reach the node through kubectl debug

Nodes have no logins and no SSH. systemd, bash, less, nftables and kmod are on every image. An
image built with `chalkos.debug.tools` adds coreutils, grep, sed, findutils, procps, iproute2 and
util-linux, and crictl on the shipped variant. The image author sets the option, and it reaches
running nodes with an upgrade to the new image.

On the shipped variant, a privileged pod on the node reaches the tools through the host's root:

```sh
kubectl debug node/w1 -it --profile=sysadmin --image=busybox -- \
  chroot /host /run/current-system/sw/bin/bash
```

`--profile=sysadmin` makes the pod privileged, which tools such as `nft` need. kubectl's default
profile leaves the pod without their capabilities. A node has no `/bin/bash`, so the command names
the bash of the system path.

This needs the node's kubelet and the API server. A node that has not joined the cluster has
`chalkctl logs`, `chalkctl node diag` and its console. On a variant without Kubernetes, the image
author adds diagnostics for its services, as [Add a diagnostic](../build/add-a-diagnostic.md)
describes.

## What next

- [Troubleshooting](troubleshooting.md) starts from a symptom and links its fix.
- [Recover a node](recover-node.md) brings back a node that does not unlock, boot or renew.
- [`chalkctl status`](../reference/cli/chalkctl_status.md),
  [`chalkctl logs`](../reference/cli/chalkctl_logs.md) and
  [`chalkctl node diag`](../reference/cli/chalkctl_node_diag.md) list their flags.
