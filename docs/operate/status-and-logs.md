---
title: "Status, logs and the console"
description: "Read what chalkctl status reports, follow a unit's journal, run the image's diagnostics, watch the console and reach a node's tools"
---

# Status, logs and the console

This page is for an operator who wants to know what a node is doing. It explains each line of
`chalkctl status`, then the four places that tell more: a unit's journal, the image's
diagnostics, the serial console and a shell on a node with the debug tools. The examples read the homelab's `node1`, a control plane.

At the machine itself, the console on its screen shows the same status, the node's network,
storage and journal, without a workstation or a network, as
[The console on the machine's screen](console.md) describes.

## Before you begin

You need the workstation that [Set up your workstation](workstation.md) describes.
`chalkctl status`, `chalkctl logs` and `chalkctl node diag` need a reader client file or the
secrets file. The diagnostics that send traffic from the node or show its firewall need an
operator's. The boot and the serial console need the machine's BMC, a screen, or
`chalklab console` in a lab.

## Read chalkctl status from the top

`chalkctl status` asks the node's chalkd for what it checks, and compares the node's config and
image with the inventory. A healthy `node1` reads:

```console
$ chalkctl status node1 --secrets secrets.age
config <version> (the inventory's)
platform metal (the inventory's)
image <image>, booted from chalkos_1.efi
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
OUTPUT    HASH    STATE
bgp       <hash>  rendered
chrony    <hash>  rendered
networkd  <hash>  rendered
kubernetes controlplane: bootstrapped, node ready: True, vip holder, control plane current
kubernetes versions: API server 1.37.2, kubelet 1.37.2, control-plane pods current
certificates:
  node                      expires <date>
  OS CA                     expires <date>
  node CA                   expires <date>
  Kubernetes CA             expires <date>
  front-proxy CA            expires <date>
  etcd CA                   expires <date>
  Kubernetes control plane  expires <date>
  kubelet client            expires <date>
  kubelet serving           expires <date>
trust:
  OS CA                 <fingerprint>
  Kubernetes CA         <fingerprint> (issues)
  front-proxy CA        <fingerprint> (issues)
  etcd CA               <fingerprint> (issues)
  service-account keys  <fingerprint> (issues)
  encryption keys       <fingerprint> (issues)
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

## Read the config line

`config <version> (the inventory's)` means the node runs the config the inventory gives it. The
parentheses say otherwise when they differ:

- `not the inventory's; chalkctl config apply node1 delivers it`: the node's files changed since the
  last delivery.
- `not the inventory's; chalkctl config apply node1 delivers it if the image <image> in the
  inactive slot, which is not the inventory's, accepts it too`: the node runs the inventory's
  image, but another image is staged or active, as an upgrade with `--until stage` of an image the
  inventory names no longer leaves it. A delivery writes that image's config slot too, so that
  image checks the config, and no command removes it. An upgrade to a new image replaces it.
- `not the inventory's; chalkctl upgrade --nodes node1 delivers it with the image <image>`: the
  node runs another image than the inventory's. `config apply` checks the config against the
  image the node runs, which may refuse it, so the config comes with the upgrade.
- `not the inventory's; it comes with the image <image> in the inactive slot`: the node has the
  inventory's image staged or active, and boots it with the config the `staged config` line shows.
- `not the inventory's; the node boots the image <image> next with the config it last ran; once it
  runs it, chalkctl config apply node1 delivers it`: the inventory's image is the known-good image
  of a rollback from a boot that was not found healthy. It keeps the config it last ran.
- `not the inventory's; the node takes no delivery until the trial of config <version> ends`: a
  config is on trial. A trial whose rollback at the deadline failed holds nothing back, as the
  node finishes that rollback before the next delivery.
- `not the inventory's; the node takes no delivery until chalkctl config rollback node1 removes
  the record of a config's trial that cannot be read`: the node cannot read its trial record and
  refuses every change but a rollback.
- `not compared with the inventory's, which takes Secret values from sops or age sources that
  status does not decrypt; chalkctl config apply --diff node1 compares them`: a `Secret` of the
  node's config takes a value from a `sops://` or `age://` source. The config's version hashes
  that value, and status decrypts nothing, so it asks for no key and cannot compare the versions.
  `--diff` decrypts the sources and compares the documents.
- `cannot read the config the inventory gives it: ...`: chalkctl could not build the node's
  documents from the inventory, for the reason it names.
- `no inventory to compare it with`: chalkctl found no inventory: neither `--inventory` nor
  `$CHALKOSINVENTORY` names one, and there is no `chalkos.yaml` in the current directory.

`config none` is a node installed before config documents existed. A node with an image staged or
active in its inactive slot adds `staged config <version> (...)`, the config that image boots
with. When it differs, its parentheses name `chalkctl config apply node1`, which writes the config
slots of both images, or `chalkctl config apply --staged node1` when the running image runs the
inventory's config already, or when the staged image is the inventory's and the running image is
not. When neither image is the inventory's, they name `chalkctl upgrade --nodes node1`, as the
config line does. A staged image that is not the inventory's on a node that runs the inventory's
image adds the same condition as the config line. When status cannot compare the config, the
staged line names `chalkctl config apply --staged --diff node1`, which compares the staged slot.
After a rollback from a boot that was not found healthy, the staged image is the rollback image,
and the parentheses say that it keeps the config it last ran until the boot is found healthy.
During a trial the node takes no delivery. When the inventory's config is on trial, the
parentheses name `chalkctl config confirm node1`, because the confirmation writes the staged
config. Otherwise they say what must end the trial first, as the config line does.

A node that holds config for its next upgrade adds
`pending config <version> (unchecked, since <time>; the next upgrade checks it against its image)`.
The time is when the node stored the config, in RFC 3339.
[A staged image and the next upgrade take config of their own](../how-it-works/node/config.md#a-staged-image-and-the-next-upgrade-take-config-of-their-own)
explains the pending config.

`config <version>` is the version stored in the running image's slot. `activated config
<version>` is the last version whose live delivery finished its activation work. An apply
can update the stored version before a later step fails. While a delivery is running,
failed, or interrupted, the activation line shows its target version and the current or
failed step. A completed activation from a previous boot is labelled as such. A node that
has only loaded config at boot may have no completed live activation recorded. `the config
activation record could not be written` follows when chalkd failed to write the record the last
time it tried, and the journal says why; the activation line may then be out of date.

`previous config <version> (config rollback restores it)` names the config the running
image ran before the last delivery.
[Roll back a config change](change-config.md#roll-back-a-config-change) delivers it again. The line
is missing until a delivery replaced the config the image was installed or upgraded with.

A node that runs a config on trial adds a line after the `config` line:

```text
config <version> on trial until <deadline>: it rolls back to config <previous-version> unless config confirm confirms it
```

The line gains an ending when one of these applies:

- `; its health check leaves out <unit>, ...`: the units that `--ignore-unit` named for this trial.
  The check leaves them out besides the units the image ignores.
- `; an attempt to roll the config back failed and is tried again each minute: <error>`: the
  rollback at the deadline failed, and the node keeps the trial open. `config confirm`
  still keeps the config.
- `; an attempt to roll the config back failed after it changed the node and is tried again each
  minute: <error>; config confirm no longer keeps the config, and config rollback
  finishes the rollback now`: the failed attempt changed the node, so only a rollback ends the
  trial.

`the record of a config's trial cannot be read: <why>; other changes are refused until
config rollback restores the previous config` takes the line's place when the node cannot
read the record of its trial.
[Try a config change](change-config.md#try-a-config-change) describes trials.

## Read the platform and image lines

`platform metal (the inventory's)` names the platform the node's image was built for. A node
whose image in the inventory is built for another platform shows
`platform kvm, but ...; changing a node's platform is a reinstall`.

The image line names the image the node booted and its boot entry. A boot that the health check
still checks ends in `, not found healthy yet`. More lines follow after an upgrade or a rollback:

- `upgrade to <image> staged; node activate makes it boot next`: an upgrade with `--until stage`
  staged the image, and the node boots its current image until `chalkctl node activate`
  activates it.
- `upgrade to <image> active; it boots next`: an upgrade with `--until activate`, or
  `chalkctl node activate`, made the image boot next, and the next reboot boots it.
- `rollback to <image> staged; it boots next`: `chalkctl upgrade --rollback --until activate`
  made the image the node ran before boot next.
- `previous image <image> (upgrade --rollback boots it)`: the inactive slot still holds the image
  the node ran before, which
  [Go back to the previous image](upgrade.md#go-back-to-the-previous-image) boots again. The line
  is missing after an install, after a fallback and while another image is staged.
- `upgrade to <image> failed: rolled back to <image>`: the new image was not found healthy, and
  the node booted its previous one. The last 30 lines that chalkd and the health check logged in
  the failed boot follow, indented.
  [A node rolled back during the upgrade](upgrade.md#a-node-rolled-back-during-the-upgrade)
  says what to do.
- `its store's root hash is ..., not that of the image ... the inventory names, ...`: the image was
  rebuilt since the node's upgrade. The line ends in `chalkctl upgrade --nodes node1 installs that
  image`, in `it has ... staged: chalkctl node activate node1 makes it boot next` when the image
  waits for its activation, or in `it has ... active, which it boots with its next reboot:
  chalkctl node reboot node1` when it waits for a reboot. After a rollback from a boot that was
  not found healthy, the inventory's image can be the known-good image the node falls back to,
  and the line says `it has ... active as its known-good image, which it boots with the config it
  last ran at its next reboot: chalkctl node reboot node1`.

`image ...; its boot is unknown: ...` means chalkd could not read the boot entries on the ESP or
the running store, for the reason named.

## Read the volume table

Each row names a volume, the disk it lives on, its mount point and its state:

- `mounted`: the volume is mounted.
- `present`: the volume exists and has no mount point, as a raw volume does.
- `not mounted`: the volume exists, but its mount failed or its disk is gone.
- `missing`: the node never created the volume's partition.

A line `disk <name>: <problem>` after the table names a disk chalkd could not use, such as
`disk longhorn: pinned disk ... is missing`.
[Add storage volumes](storage-volumes.md#if-something-goes-wrong) covers those problems.

## Read the output table

An output is a set of files that the image renders from the config, such as chrony's sources.
Each row names an output, the hash of the files the node serves, and its state:

- `rendered`: the last render and its check passed.
- `failed: <error>`: the last render or its check failed, for the reason named.
- `not rendered yet`: no render ran yet. The hash column shows `none`.

The image author declares the outputs, as
[Render config into files](../build/render-config.md) describes.

## Read the Kubernetes line

With the Kubernetes layer, `kubernetes worker: joined, node ready: True` names the node's kind, its
state and whether its Node is Ready. A control plane adds its VIP role, `vip holder` or
`vip standby`, and `control plane current` when its API server and etcd serve the current
certificates and the API server answers ready. A Node that is not Ready adds the reason and the
message of its Ready condition, such as
`node ready: False (KubeletNotReady: container runtime network not ready)`.
[Kubernetes status states](../reference/kubernetes.md#kubernetes-status-states)
lists every state, and [Troubleshoot Kubernetes](../kubernetes/troubleshooting.md) acts on them.

The line after it, `kubernetes versions: API server 1.37.2, kubelet 1.37.2`, names the Kubernetes
version of the API server the node's config names and the release of its image's kubelet. A
bootstrapped control plane adds `control-plane pods current` once its API server, controller-manager
and scheduler run the images its config names and are Ready, or says which of them does not yet.

A node with pods that do not run lists them after these lines, with each pod's phase and reason:

```text
pods not running:
  web/pending-pod  Pending  -
  web/crash        Running  CrashLoopBackOff
  and 1 more
```

A pod counts when its phase is neither `Running` nor `Succeeded`, or when it runs with a container
that waits. The reason is that of the first init container that waits or failed, prefixed `Init:`,
else of the first container that waits, else the pod's own. A failed pod of a Job is left out, as
the Job's record of a run. Pending pods come first, then running ones, then failed ones. The list
holds at most 20 pods and counts the rest in `and <n> more`. `pods unknown: <error>` means the node
could not list its pods.

## Read the certificates section

Each line names a certificate the node holds or issues and its expiry. A third column names what
needs doing:

- `less than a third of its lifetime remains`: the node certificate is due for renewal, and a node
  of a Kubernetes cluster renews it through a control plane.
- `the node certificate expires on <date>; this image cannot renew it on its own; run chalkctl node renew router`:
  the node certificate is due for renewal on a node whose image runs no Kubernetes, such as a
  router. Run the command it names. Once the certificate has expired, the line says `expired on`.
- `renewal failing: <reason>; expires <time>`: the node could not renew through a control plane.
  Fix what the reason names, often a cluster endpoint that does not reach chalkd on port 50000.
- `expired`: the certificate expired.
  [The node certificate expired](recover-node.md#the-node-certificate-expired) brings the node
  back.
- `OS CA expires <date>`: the OS CA has less than a year left.
  [Rotate the OS CA](access.md#rotate-the-os-ca) replaces it.
- `node CA expires <date>; run chalkctl rotate node-ca`: the node CA has less than 18 months left.
  [Rotate the node CA before it expires](access.md#rotate-the-node-ca-before-it-expires) covers
  it.

`expiry unknown` in the second column means the node could not read the certificate's expiry.

With the Kubernetes layer, the Kubernetes certificates follow: its CAs, a control plane's own
certificates and the kubelet's. `Kubernetes CA expires <date>` warns a year ahead.
`less than a tenth of its lifetime remains, though the kubelet renews it itself` means the kubelet
did not renew its certificate. [Troubleshoot Kubernetes](../kubernetes/troubleshooting.md) and
[Rotate the Kubernetes CAs](../kubernetes/rotate-kubernetes-cas.md) cover these lines.

## Read the trust section

Each line names a CA or key set the node trusts, by the first 16 hex characters of each
fingerprint. `(issues)` marks the one that signs new certificates. Two fingerprints on one line
are normal during a rotation and wrong outside one.
[Manage access and certificates](access.md#act-at-the-pauses-of-a-rotation) explains the
phases.

## Read the time line

`time: synchronised to <server>, offset <offset> s` means chrony synchronised the clock. A node
whose clock is not synchronised shows
`time: not synchronised; certificates are checked against this clock`. chrony reached none of its
servers. `time: unknown: ...` means chalkd could not ask chrony.

Certificates start an hour before they are issued, so a small offset does no harm. A clock that is
days off rejects valid certificates. With the default servers, NTS needs the node to reach them on
TCP port 4460 and UDP port 123. A network that blocks both needs its own servers, as
[Name the time servers](node-config.md#name-the-time-servers) shows.

## Read a failed unit line

`failed unit <unit>` names each systemd unit that failed in this boot. A healthy node has none.
Read the unit's journal next.

## Follow a unit's journal with chalkctl logs

`chalkctl logs` prints the node's journal of the current boot, of every unit or of `--unit`
alone. `-f` follows it:

```sh
chalkctl logs node1 --unit=chalkd.service -f
```

These units cover most of a node's own work:

| Unit | Does |
| --- | --- |
| `chalkd.service` | The node agent: installs, upgrades, config, certificates and storage. |
| `chalkos-config.service` | Applies the node's config at boot: hostname, network, time servers and outputs. |
| `chalkos-health.service` | Decides whether a boot after an upgrade is healthy. |
| `chalkos-chrony-dhcp.service` | Passes time servers that DHCP offers to chrony. |

The Kubernetes layer adds `chalkos-kubernetes.service`, which prepares the node's addresses and
Kubernetes files before the kubelet starts, and `kubelet.service` and `containerd.service`.
`chalkos-registry-credentials.service` and `chalkos-registry-mirrors.service` write the
credentials and mirrors for the pods' image pulls. `chalkos-registry.service` serves the cluster
registry on the nodes that a `ClusterRegistry` names.

`chalkctl logs` shows the current boot only. For a boot that fell back after an upgrade,
`chalkctl status` shows the lines the failed boot logged.

A node in maintenance mode answers `chalkctl logs` too, with `--fingerprint` or `--insecure`:

```sh
chalkctl logs node1 --insecure --unit=chalkd.service
```

## Run the image's diagnostics with chalkctl node diag

A diagnostic is a read-only command that the node's image declares, such as `ip route get`.
chalkd runs it on the node and sends back what it prints, so a node without a login still shows
its addresses, routes and firewall. Without a diagnostic, `chalkctl node diag` lists them:

```console
$ chalkctl node diag node1 --secrets secrets.age
DIAGNOSTIC            ROLE      DESCRIPTION
addresses             reader    The addresses of the node's interfaces, as ip -json address show prints them.
bfd-peers             reader    One VRF's BFD sessions and their state, as show bfd peers prints them.
  [vrf=<pattern>]               VRF to ask about: default for the default VRF, or a router's VRF by the name of its VRF document. Matches ^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$. Default: default.
bgp-neighbor          reader    One BGP session in detail, as show bgp neighbors prints it: its state, timers, capabilities and routes.
  peer=<pattern>                The neighbour's address or the session's interface, as bgp-summary lists it. Matches ^([0-9A-Fa-f:.]+|[A-Za-z0-9_.:@-]{1,15})$.
  [vrf=<pattern>]               VRF to ask about: default for the default VRF, or a router's VRF by the name of its VRF document. Matches ^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$. Default: default.
bgp-route             reader    One VRF's BGP paths to a prefix, as show bgp prints them: each path's AS path, next hop and attributes, and the best.
  prefix=<prefix>               Address range of the family, such as ::/0.
  [vrf=<pattern>]               VRF to ask about: default for the default VRF, or a router's VRF by the name of its VRF document. Matches ^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$. Default: default.
  [family=<enum>]               Address family of the prefix. One of ipv4, ipv6. Default: ipv6.
bgp-summary           reader    One VRF's BGP sessions of each address family, as show bgp summary prints them.
  [vrf=<pattern>]               VRF to ask about: default for the default VRF, or a router's VRF by the name of its VRF document. Matches ^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$. Default: default.
dns-query             operator  Looks up a name in DNS, through the node's resolver or the server given.
  name=<hostname>               Name to look up.
  [type=<enum>]                 Record type; A when left out. One of A, AAAA, CAA, CNAME, DS, DNSKEY, MX, NS, PTR, SOA, SRV, TXT.
  [server=<address>]            Address of the DNS server to ask; the node's resolver when left out.
ip-rules              reader    The routing policy rules of one family, as ip -json rule show prints them: which table the kernel looks a packet's route up in, in the order of their priorities.
  [family=<enum>]               Address family: inet for IPv4, inet6 for IPv6. One of inet, inet6. Default: inet.
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
firewall, so they need an operator. A parameter in brackets may be left out. `bfd-peers`,
`bgp-neighbor`, `bgp-route` and `bgp-summary` come with FRR, which the Kubernetes layer's BGP
speaker runs. Every image has the others.

Name the diagnostic and give each parameter as `<parameter>=<value>`:

```sh
chalkctl node diag node1 route-get address=2001:db8::1
chalkctl node diag node1 routes family=inet6
chalkctl node diag node1 tcp-connect address=192.0.2.10 port=443
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
chalkctl node diag node1 --insecure tcp-connect address=2001:db8::5000 port=443
```

An image built without an OS CA accepts any client in maintenance mode and treats it as an admin.
A caller without a certificate may then run every diagnostic, `nft-ruleset` and the other
operator diagnostics included. It may call Install and Reboot as well. An image with the OS CA
takes only the client files of its cluster in maintenance mode, each with its own role.

## Watch the serial console

The machine's screen and its serial console show the boot: the boot loader, the kernel's
messages and a passphrase prompt for an encrypted disk. systemd's boot status and the emergency
shell appear on the last of the image's kernel consoles alone: the screen on `metal` and the
installer, the serial port on `kvm`, as
[Images and platforms](../build/images-and-platforms.md#choose-the-console-people-watch)
describes. Once chalkd serves, the screen shows [the console](console.md) in place of a login
prompt. The serial console goes on to show chalkd's lines, which `chalkos-serial-log@` copies
there from the journal while the screen is the last console. In maintenance mode, chalkd prints
its certificate's fingerprint and the node's addresses there:

```text
chalkd: maintenance mode, accepting clients of the OS CA; certificate fingerprint <fingerprint>
chalkd: addresses 192.168.1.50; certificate fingerprint <fingerprint>
```

On an image with the console, the kernel prints only emergencies on its consoles, so the serial
console shows a panic or an oops but no other kernel message. The journal keeps them all.

On hardware the serial console is the BMC's serial-over-LAN. A VM of the `kvm` platform writes
its console to the serial port alone. In a lab, `chalklab console` prints a VM's serial console
and follows it:

```sh
chalklab console w1
```

The lab's console is read-only, so a lab node cannot answer a passphrase prompt.

## Reach the node's tools through kubectl debug

Nodes have no logins and no SSH. Every image has systemd, less, nftables and kmod, but no shell.
An image built with `chalkos.debug.tools` adds bash, coreutils, grep, sed, findutils, procps,
iproute2 and util-linux, and crictl with the Kubernetes layer. The image author sets the option,
and it reaches running nodes with an upgrade to the new image.

With the Kubernetes layer and the debug tools, a privileged pod on the node reaches the tools
through the host's root:

```sh
kubectl debug node/node1 -it --profile=sysadmin --image=busybox -- \
  chroot /host /run/current-system/sw/bin/bash
```

`--profile=sysadmin` makes the pod privileged, which tools such as `nft` need. kubectl's default
profile leaves the pod without their capabilities. A node has no `/bin/bash` and no `/bin/sh`, so
the command names the bash that the debug tools put on the system path.

This needs the node's kubelet and the API server. A node that has not joined the cluster has
`chalkctl logs`, `chalkctl node diag` and its console. On an image without the Kubernetes layer, the
image author adds diagnostics for its services, as [Add a diagnostic](../build/add-a-diagnostic.md)
describes.

## What next

- [Troubleshooting](troubleshooting.md) starts from a symptom and links its fix.
- [The console on the machine's screen](console.md) reads the node at its machine.
- [Recover a node](recover-node.md) brings back a node that does not unlock, boot or renew.
- [`chalkctl status`](../reference/cli/chalkctl_status.md),
  [`chalkctl logs`](../reference/cli/chalkctl_logs.md) and
  [`chalkctl node diag`](../reference/cli/chalkctl_node_diag.md) list their flags.
