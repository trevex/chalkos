---
title: "The console on the machine's screen"
description: "Read a node at its screen: its identity and health, its status, network, storage, logs and layers, and the actions for a node chalkctl cannot reach"
---

# The console on the machine's screen

This page is for an operator at a node's machine: in person, through a BMC's remote screen, or
through a hypervisor's display. The machine's screen, tty1, shows the console in place of a login
prompt. It shows the node's identity and health, its status, network, storage and logs, and the
tabs of the image's layers. Its actions reboot, shut down or roll back a node that chalkctl cannot
reach.

Every image and the installer run the console. It is the program `chalkos-console`, which reads
the node through chalkd on a local socket, so it needs no network and no client file.

## Before you begin

You need the machine's screen and keyboard, or a BMC or hypervisor that shows them. The serial
console is a separate console. It shows the boot loader, the kernel's messages, the passphrase
prompts of encrypted volumes and chalkd's lines, as
[Status, logs and the console](status-and-logs.md#the-serial-console-shows-the-boot-and-chalkds-lines)
describes. The `kvm` platform writes the boot to the serial console alone. The VMs of a lab have
no screen, so `chalklab console` shows their serial console.

The console starts once chalkd runs and the passphrase prompts of the boot are answered. Until
then the screen shows the boot.

## The header names the node and its health

The rows at the top stay on every tab. On an installed node they read:

```text
node1  node n1  normal  up 3h 12m
image 81bd81bd81bd generation 7  worker on metal
healthy
 1 Overview   2 Network   3 Storage   4 Logs
```

The first row names the hostname, the node's name, its mode and how long the machine has run.
The second names the image by the first 12 hex characters of its root hash, its boot entry's
generation, and the variant and platform it was built for. The last row lists the tabs, the one on
screen highlighted.

The third row is the node's health. It reads `healthy`, or lists what needs attention, separated by
semicolons:

- `<n> failed units: <unit>, ...`: units that failed in this boot.
- `config <version> on trial until <time>`: a config on trial, which rolls back at that time
  unless `chalkctl config confirm` confirms it.
- `the record of a config's trial cannot be read`: the node takes no other change until a config
  rollback.
- `rollback to <image> boots next`: the next boot rolls back to the image the node ran before.
- `chalkd does not answer, retrying: <error>`: the console cannot reach chalkd. The tabs keep the
  data they last read, and the console asks chalkd again every 2 seconds.

In maintenance mode and on the installer, the header keeps the SHA-256 fingerprint of chalkd's
certificate and the node's addresses on screen, which `chalkctl install` needs:

```text
nixos  maintenance  up 3h 12m
image 81bd81bd81bd
maintenance mode: chalkctl install installs this node
fingerprint <fingerprint>
addresses 192.168.1.50 2001:db8::50
 1 Overview   2 Network   3 Storage   4 Logs
```

`<fingerprint>` is 64 hex characters, and a screen narrower than the fingerprint breaks it over
several rows. The addresses are every address of the machine beyond link-local ones. The console
reads them every 5 seconds, and every 30 seconds once they have stayed the same for a minute. The
installer's health row reads `installer: chalkctl install installs nodes from here`.
[Install a node](install.md#note-the-address-and-the-fingerprint) uses both values.

## Overview shows the node's status

Overview shows what `chalkctl status` prints, read every 2 seconds:

```text
config 3fa9c1e2b7d04e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f4e5f
previous config 1b2c3d4e5f607a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b7a8b
platform metal
image 81bd81bd81bd (v1.4.0), booted from chalkos_7.efi
previous image 3fa93fa93fa9 (v1.3.0) (upgrade --rollback boots it)
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
data    data    /srv/data    not mounted
OUTPUT    HASH          STATE
chrony    c1c1c1c1c1c1  rendered
networkd  e3e3e3e3e3e3  rendered
certificates:
  node   expires 2027-01-01
  OS CA  expires 2027-01-01
trust:
  OS CA  cdcdcdcdcdcdcdcd (issues)
time: synchronised to 192.168.1.1, offset +0.000123 s
```

The console has no inventory, so the lines do not compare the node with it. The layers' sections
are on their own tabs. [Status, logs and the console](status-and-logs.md) explains each line. A
node in maintenance mode or the installer has no status, and Overview says so.

When a read fails, a tab keeps the data it read last and names the error above it, with the time of
that data:

```text
status: deadline_exceeded: chronyc timed out; shown as of 15:04:05
```

## Network shows the links, routes and DNS servers

Network lists each link with its state, MAC address, MTU and addresses, then the default routes of
the main table with their gateway, link and protocol, and the DNS servers the node asks:

```text
LINK    STATE  MAC                MTU   ADDRESSES
enp1s0  UP     52:54:00:12:34:56  1500  192.168.1.50/24
                                        2001:db8::50/64
                                        fe80::5054:ff:fe12:3456/64
enp2s0  DOWN   52:54:00:ab:cd:ef  1500
default routes:
  via 192.168.1.1 dev enp1s0 (dhcp)
  via fe80::1 dev enp1s0 (ra)
DNS servers: 192.168.1.1, 2001:db8::1
```

The tab runs the diagnostics `addresses` and `routes` every 5 seconds while it is on screen, and
reads the DNS servers from systemd-resolved.

## Storage shows the disks and the volumes

Storage lists the disks, then each volume with its encryption and how much of it is used:

```text
DISK      SIZE  TYPE  MODEL          SERIAL      USE
/dev/vda  16G   hdd   QEMU HARDDISK  chalk-sys   boot
/dev/vdb  8G    hdd   QEMU HARDDISK  chalk-data  disk data
VOLUME  DISK    MOUNT POINT  ENCRYPTION  USE
var     system  /var         tpm2        1.2G of 9.8G (12%)
data    data    /srv/data    none        not mounted
```

A volume that is not mounted shows its state in place of its use. In maintenance mode and on the
installer, the tab lists the disks and their partitions as
[`chalkctl disks`](../reference/cli/chalkctl_disks.md) does.

## Logs follows the journal of this boot

Logs shows the journal of the current boot and follows it. The first row says what it shows and
lists the tab's keys:

```text
this boot, all units, following   u unit  / search  n N match  w wrap
2026-11-13T11:52:01+00:00 node1 systemd[1]: Started chalkd.service.
2026-11-13T11:52:02+00:00 node1 chronyd[580]: chronyd version 4.6 starting (+CMD
2026-11-13T11:52:02+00:00 node1 chalkd[612]: serving the node API on :50000
2026-11-13T11:53:15+00:00 node1 chalkd[612]: apply: output networkd failed: rend
2026-11-13T11:55:00+00:00 node1 chalkd[612]: config 3fa9c1e2b7d0 delivered
```

The tab starts to read the journal the first time it comes on screen and keeps its last 5000
lines. Lines with `error`, `failed`, `fail`, `fatal` or `panic` show in red.

- `u` opens the list of active services. Enter shows the journal of the one selected, or of every
  unit with `all units`. Esc closes the list.
- `/` starts a search. Type the text and press Enter: the tab shows the first line that holds it,
  ignoring case, and the first row counts the matches. Esc while you type ends the search and
  clears it.
- `n` and `N` go to the next and the previous match.
- `w` wraps long lines at the screen's width, or cuts them again.
- Scrolling up stops following the journal. `End` goes back to the last line and follows again.

The tab reads the journal through chalkd. While chalkd does not answer, or its stream ends, the
tab runs `journalctl` itself and says so in its first row:

```text
this boot, all units, following; read by the console: chalkd does not answer
```

It asks chalkd again after 10 seconds, then after twice as long each time, up to every 2 minutes,
and goes back to chalkd's journal once chalkd sends it.

## Kubernetes shows the node's part in the cluster

Images with the Kubernetes layer add a Kubernetes tab. Once the node is installed, it shows the
layer's section of `chalkctl status`, the revoked nodes a control plane lists, and the pods on the
node that do not run:

```text
kubernetes controlplane: bootstrapped, node ready: True, vip holder, control
  plane current
kubernetes versions: API server 1.37.1, kubelet 1.37.1, control-plane pods
  current
pods not running:
  apps/migrate-7c2b: Pending
  web/crash-5d9f: Running CrashLoopBackOff
  and 21 more
```

[The Kubernetes line shows the node's part in the cluster](status-and-logs.md#the-kubernetes-line-shows-the-nodes-part-in-the-cluster)
explains the lines. The tab reads the status every 5 seconds while it is on screen and every 30
seconds while it is not, because the warning before a reboot needs the node's kind.

## Router shows the sessions, leases and tunnels

Router images add a Router tab. Once the node is installed, it shows the router's section of
`chalkctl status`, then the DHCP leases and the WireGuard interfaces with their peers:

```text
bgp:
  VRF      PEER      NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD
  default  fabric    eth1           Established  1h2m3s  1         2     up
  default  upstream  203.0.113.254  Active       -       0         0     -
nat64:
  INSTANCE  SESSIONS  BINDINGS  POOL TCP  POOL UDP  POOL ICMP
  edge      2         2         4535      4535      -
dhcp4 default: running
  SUBNET  RANGE         LEASED  POOL
  office  192.0.2.0/24  2       100
nat64 sessions: chalkctl node diag n1 nat64-sessions instance=<name> lists them
dhcp leases:
  VRF      SUBNET  ADDRESS     MAC                HOSTNAME  ENDS
  default  office  192.0.2.10  52:54:00:aa:bb:01  laptop    2026-11-13T19:04:05Z
wireguard:
  interface: wg0
    public key: uLRrWL5pg3FX2ydGsAsUrQ/VvWautg0OJpoJEGP12XM=
    private key: (hidden)
    listening port: 51820
```

The leases and the tunnels come from the diagnostics `dhcp-leases` and `wireguard`. A feature
the image leaves out shows no part. The status counts each NAT64 instance's sessions, and the tab
names the diagnostic that lists them. The tab reads every 10 seconds while it is on screen.

## Keys

These keys work on every tab. While the actions menu, the logs' search or their list of units is
open, it takes the keys instead.

| Key | Does |
| --- | --- |
| Tab, Right | Shows the next tab. |
| Shift+Tab, Left | Shows the previous tab. |
| 1 to 9 | Shows the tab with that number. |
| Up, Down, PgUp, PgDn, Home, End | Scroll the tab. |
| a, F10 | Open the actions menu. |

The logs' own keys are listed in [Logs follows the journal of this boot](#logs-follows-the-journal-of-this-boot).
In the actions menu, Up and Down select an action, and Enter or the action's letter picks it.
Esc, `a` or F10 closes the menu.

While a password request is on screen, it takes every key:

| Key | Does |
| --- | --- |
| Enter | Sends what you typed. |
| Esc | Cancels the request. |
| Backspace | Erases the last character. |
| Ctrl+U | Erases everything you typed. |

## The console answers password requests

A unit that needs a password after the boot asks through systemd, for example to unlock a volume
that opens late. systemd's own prompt cannot use tty1 while the console runs there, so the console
answers the request itself. The question takes the screen below the header, in place of the tabs:

```text
node1  node n1  normal  up 3h 12m
image 81bd81bd81bd generation 7  worker on metal
healthy
systemd asks for a password

Please enter passphrase for disk data:
chalkctl recovery-key n1 prints the node's recovery key

> ***_
```

The input shows a star for each character you type. A request that allows echo, such as one for a
user name, shows the text itself, and a request that asks for silence shows nothing. Enter sends
the answer and Esc cancels the request. When a volume asks, the console names the command that
prints the node's recovery key, as
[Unlock the node with its recovery key](recover-node.md#unlock-the-node-with-its-recovery-key)
describes.

When several requests wait, the console shows the oldest first and counts the others below it,
such as `1 more request waits`. The next one comes on screen as soon as you answer or cancel the
one before. A request that ends without your answer closes on the screen too: it timed out, or
someone answered it elsewhere, such as on the serial console. If an answer cannot reach the
request, the row above the keys says so and the request stays on screen. A request that is still
there 5 seconds after your answer comes back, because the unit did not take the answer.

For 300 milliseconds after a request comes on screen, Enter does nothing, so an Enter you meant
for the tab or menu before it sends no empty password. The input takes up to 2048 bytes, the
longest answer systemd reads, and ignores keys beyond that. A question too long for the screen is
cut and ends with `…`, so the input row stays visible.

The console clears what you typed once it sends the answer or the request closes, and it writes no
password to its log.

Answering does not depend on the console's actions, so `chalkos.console.actions.enable = false`
leaves it on. With `chalkos.console.enable = false` there is no console, and systemd's own prompt
asks on the kernel's consoles, as during the boot.

The prompts of the boot come before the console starts.
[The console asks for a passphrase at boot](recover-node.md#the-console-asks-for-a-passphrase-at-boot)
explains them.

## Actions reboot, shut down or roll back the node

Press `a` or F10 to open the actions menu:

```text
Actions
> r  Reboot
  s  Shut down
  i  Roll back the image     the node keeps no previous image
  c  Roll back the config

Enter or a letter: choose   Esc: close
```

| Action | Does | Mode |
| --- | --- | --- |
| Reboot | The node goes down at once and boots the image and config it runs. | maintenance and normal mode, the installer |
| Shut down | The node powers off and stays off until someone powers it on. | maintenance and normal mode, the installer |
| Roll back the image | The node reboots into the image it ran before, with the config it last ran. [`chalkctl upgrade --rollback`](upgrade.md#go-back-to-the-previous-image) does the same and drains the node first. | normal mode |
| Roll back the config | The node delivers its previous config again, and the units its effects name restart, as [`chalkctl config rollback`](change-config.md#roll-back-a-config-change) does. During a trial, it restores the config from before the trial. | normal mode |

An action that does not apply is grey, with the reason beside it:
`the node keeps no previous image`, `the node keeps no previous config`,
`the node is not installed`, `the installer runs no image of its own`, or
`the node's status is not read yet`.

Picking an action asks for a confirmation that names the node and what the action does:

```text
Roll back the config of n1 to 1b2c3d4e5f60? The node delivers it again, and the
units its effects name restart.

y: roll back the config   Enter or Esc: back
```

Only `y` runs the action. Enter, Esc and `n` go back to the list. The line above the keys shows
the result, such as `config rolled back to 1b2c3d4e5f60; 1 unit restarted`, or the error, such as
`reboot failed: ...`. One action runs at a time: while one runs, `a` shows
`an action runs; its result comes here`.

On an image with the Kubernetes layer, the confirmations of a reboot, a shutdown and a rollback of
the image warn that the console does not drain the node. On a control plane they name etcd's
quorum check too:

```text
Reboot n1? It goes down at once and boots the image and config it runs. chalkctl
node reboot and upgrade --rollback drain the node and check etcd's quorum first;
the console does neither.
```

A worker's warning reads
`chalkctl node reboot and upgrade --rollback drain the node first; the console does not.` Until
the console has read the node's kind, it gives the control plane's warning, beginning with
`The node may be a control plane:`. Where chalkctl reaches the node, use
[`chalkctl node reboot`](../reference/cli/chalkctl_node_reboot.md) or
[`chalkctl upgrade --rollback`](upgrade.md#go-back-to-the-previous-image), which drain it and on a
control plane check etcd's quorum first.

## The console reads less while nobody types

After 5 minutes without a key, the console counts as idle. It then reads the node every 30
seconds and stops following the journal. The tabs read no diagnostics and no layer status. The
row above the keys says:

```text
idle: the console reads the node every 30 s and follows no journal; a key resumes
```

Any key ends the idle time, and the console reads the node at once. The key also does what it does
otherwise.

## Kernel messages stay off the screen

An image with the console sets the kernel's console log level so that the kernel prints only
emergencies on its consoles (`kernel.printk` is `1 4 1 7`). A panic or an oops still prints
over the screen. The serial console gets only the kernel's emergencies too. The journal keeps
every kernel message, and the Logs tab and `chalkctl logs` show them.

## A console that fails restarts

The console restarts whenever it stops, after a second at first and ever more slowly while it keeps
failing, up to every 30 seconds. A console that stopped leaves no stale screen behind. It runs
without capabilities and without a network, and sees the file system read-only.

## Turn the console or its actions off

The image author turns the console off, or its actions alone, with two image options:

- [`chalkos.console.enable`](../reference/image-options.md#chalkosconsoleenable), true by default,
  runs the console on tty1. With it false, tty1 shows the getty's login prompt again, the kernel
  prints its messages at its default log level, and chalkd serves no console socket. No account
  logs in at that prompt.
- [`chalkos.console.actions.enable`](../reference/image-options.md#chalkosconsoleactionsenable),
  true by default, allows the actions. With it false, the console's last row reads
  `a actions (off in this image)`, `a` shows
  `the image turns the console's actions off (chalkos.console.actions.enable)`, and chalkd refuses
  the actions on its console socket.

Each option takes effect on a node with an upgrade to an image built with it.
[Variants and platforms](../build/variants-and-platforms.md) shows where an image's options are
set.

## Related pages

- [Status, logs and the console](status-and-logs.md) explains each line of `chalkctl status`.
- [Recover a node](recover-node.md) brings back a node that does not unlock, boot or renew.
- [Security model](../how-it-works/node/security.md#the-console-acts-for-whoever-sits-at-the-machine)
  describes what the console gives whoever sits at the machine.
- [Roles and credentials](../reference/access.md#the-console) lists what the console may call.
