---
title: "Customise the installer"
description: "Give the installer static addresses, VLANs, bonds, drivers or firmware"
---

# Customise the installer

This page is for an image author whose machines need more from the installer than DHCP and the
base drivers. At the end the cluster's installer gives a machine a static address, a VLAN or a
bond, drives its network card and storage controller, and writes its console where the BMC shows
it. One installer still serves the whole cluster.

The installer takes its addresses by DHCP on every Ethernet port and carries the drivers of the
base module groups. A machine without DHCP on its network, on a VLAN, with a bond, or with a card
the installer cannot drive needs NixOS modules of its own in the installer. The
[`installer.modules`](../reference/options.md#installermodules) argument of `chalkos.lib.mkImages`
adds them. They change the installer alone: the variants' images and the installed nodes stay as
they are. `mkImages` builds one installer, which installs the images of every platform.

The settings go into the installer's image because the kernel command line is part of the UKI.
Under Secure Boot the UKI is signed, so the boot menu cannot add `ip=` or a driver option, and
nothing on the medium can change after signing. Whatever differs between machines goes into the
installer and is matched at boot by MAC address or interface name. Each machine then finds the
network configuration written for it, and the others fall back to DHCP.

## Before you begin

You need:

- The flake that builds the cluster's images with `mkImages`, with
  [`osCA`](../reference/options.md#osca) set.
- The MAC addresses of the machines' ports, from the BMC, the firmware setup or the machines'
  labels.
- For machines that enforce Secure Boot, the db key and certificate of
  [Sign images for Secure Boot](sign-images.md).

[Install a node](../operate/install.md#boot-the-machine-into-maintenance-mode) builds the unchanged installer.

## Add a module to the installer

The installer's modules are ordinary NixOS modules in a file of their own, `installer.nix`, which
the sections below fill. Name it in the call of `mkImages` in `flake.nix`, and expose the
installer to build:

```nix
images = chalkos.lib.mkImages {
  osCA = ./secrets.pub.json;
  platforms = [ "metal" ];
  installer.modules = [ ./installer.nix ];
};
```

```nix
packages.x86_64-linux.installer = images.installer;
```

The installer does not carry `modules`, the modules every variant gets. It holds no config, so
modules that read config have nothing to do there.

## Give a machine a static address

Write a network file per machine, matched by the MAC address of its port:

```nix title="installer.nix"
{
  systemd.network.networks."10-cp1" = {
    matchConfig.MACAddress = "3c:ec:ef:00:00:11";
    address = [ "10.0.0.11/24" ];
    gateway = [ "10.0.0.1" ];
    dns = [ "10.0.0.1" ];
  };
}
```

The installer configures its network with systemd-networkd. Its default network file,
`99-ethernet-default-dhcp`, runs DHCP on every Ethernet port. networkd applies the first file in
name order that matches a port, so `10-cp1` takes the port over.

The options are NixOS's
[`systemd.network`](https://nixos.org/manual/nixos/stable/options#opt-systemd.network.networks)
options, whose sections and keys a node's `Network` documents carry in camelCase. Give a machine
the address its node has after the install. chalkctl then reaches it at the address the inventory
names for the node, without `--address`.

## Put the installer on a VLAN

A VLAN is a network device of its own, declared as a netdev and attached to the port that
carries it:

```nix title="installer.nix"
{
  systemd.network.netdevs."20-vlan42" = {
    netdevConfig = {
      Kind = "vlan";
      Name = "vlan42";
    };
    vlanConfig.Id = 42;
  };
  systemd.network.networks."10-uplink" = {
    matchConfig = {
      MACAddress = "3c:ec:ef:00:00:11";
      # The VLAN device has the port's MAC address too; only the port is of type ether.
      Type = "ether";
    };
    vlan = [ "vlan42" ];
    networkConfig.LinkLocalAddressing = "no";
  };
  systemd.network.networks."30-vlan42" = {
    matchConfig.Name = "vlan42";
    DHCP = "yes";
  };
}
```

The port's own file takes it from the default DHCP file and runs no DHCP there. The VLAN device
gets its address by DHCP here; give it `address` and `gateway` instead for a static one.

A VLAN device inherits its port's MAC address, so a port file that matched by MAC address alone
would match `vlan42` too and, as it sorts first, take it from its own file. `Type = "ether"`
limits the match to the physical port.

## Bond ports

A bond is a netdev too, with the ports matched by their MAC addresses, which networkd takes as a
space-separated list, and by `Type = "ether"`, because the bond takes the MAC address of one of
its ports:

```nix title="installer.nix"
{
  systemd.network.netdevs."20-bond0" = {
    netdevConfig = {
      Kind = "bond";
      Name = "bond0";
    };
    bondConfig = {
      Mode = "802.3ad";
      TransmitHashPolicy = "layer3+4";
    };
  };
  systemd.network.networks."10-bond0-ports" = {
    matchConfig = {
      MACAddress = "3c:ec:ef:00:00:21 3c:ec:ef:00:00:22";
      # The bond takes a port's MAC address; only the ports are of type ether.
      Type = "ether";
    };
    networkConfig.Bond = "bond0";
  };
  systemd.network.networks."20-bond0" = {
    matchConfig.Name = "bond0";
    DHCP = "yes";
  };
}
```

`802.3ad` needs LACP on the switch ports. Without it, use `active-backup`, which needs nothing
from the switch. A VLAN on the bond goes into the bond's file as `vlan = [ "vlan42" ];`, as above.

## Add drivers and firmware

The installer carries the base module groups, `storage`, `network`, `virtualisation`,
`filesystems`, `kubernetes` and `platform`, which hold the in-tree Ethernet, NVMe, SATA, SAS and
virtio drivers. Add a group or single modules, and firmware files, the way an image's modules add
them:

```nix title="installer.nix"
{ pkgs, ... }:
{
  chalkos.kernel.moduleGroups = [ "infiniband" ];
  chalkos.kernel.extraModules = [ "kvm_amd" ];
  hardware.firmware = [ pkgs.linux-firmware ];
}
```

The build fails on a module name the kernel does not have, so a typo shows at build time.
`linux-firmware` holds the firmware of every vendor and adds several hundred MiB.
[Add software, drivers and kernel modules](add-software.md) shows how to find the right module and
take only the firmware files a card needs. The installer only needs to reach the network and see
the target disk. The image the node runs needs the same drivers, in its variant's or platform's
modules.

## Change the console

The installer writes its console to the screen and to the first serial port, `ttyS0`. Linux sends
kernel messages to every console on the command line. chalkd's lines, with the fingerprint and
the addresses, go to the last one only. A machine whose BMC shows serial-over-LAN on the second
port needs that port last:

```nix title="installer.nix"
{ lib, ... }:
{
  boot.kernelParams = lib.mkAfter [ "console=ttyS1,115200" ];
}
```

`lib.mkAfter` places the entry after the installer's own `console=` entries. A plain list would
come before them, leaving `ttyS0` last.

## The installer keeps chalkd in installer mode

The modules may change anything a NixOS module can, except three things that make the image the
installer. The build fails with a message when a module disables `chalkd.service`, changes its
`CHALKD_INSTALLER` environment variable, or sets `chalkos.platform.name`. The installer runs
chalkd in installer mode, and installs images of every platform.

## Build the installer and sign a copy

Build the installer:

```console
$ nix build .#installer -o installer
$ ls installer
chalkos-installer.iso  chalkos-installer.iso.json  chalkos-installer.raw  metadata.json  repart-output.json
```

Machines without Secure Boot boot `installer/chalkos-installer.iso` as it is. For machines that
enforce Secure Boot, sign a copy, as
[Sign images for Secure Boot](sign-images.md#sign-the-installer) does:

```sh
rm -rf installer-signed
chalkbuild sign --key db.key --cert db.crt installer installer-signed
```

[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) signs the copy's raw image and ISO. It
refuses an output directory that exists, so the first command removes the copy of an earlier
build. Write `installer-signed/chalkos-installer.iso` to the stick.

Sign the installer with the key that signs the nodes' images. The installer seals each node's
STATE to PCR 7 as measured under the installer, so an image signed with another key asks for the
recovery key on its first boot.

The installer carries the OS CA of the `secrets.pub.json` that the `osCA` argument names. It
accepts only clients whose certificate chains to that CA. After an OS CA rotation with
[`chalkctl rotate`](../reference/cli/chalkctl_rotate.md), build the installer and the images again,
and sign them again where the nodes need it, because images carry the OS CA too. An installer or
image in maintenance mode built before the rotation refuses chalkctl's new certificate, and
chalkctl says so:
`the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's
certificate; build the image and the installer media again from the current secrets.pub.json`.

## Check that it worked

Boot a machine from the new installer. Its console shows the address you gave it, or the one
DHCP gave the VLAN or the bond, with the fingerprint:

```text
chalkd: addresses 10.0.0.11; certificate fingerprint <fingerprint>
```

[`chalkctl disks`](../reference/cli/chalkctl_disks.md) lists the machine's disks, which shows the
installer reaches the network and drives the storage controller:

```sh
chalkctl disks --address=10.0.0.11 --fingerprint=<fingerprint> --secrets=secrets.age
```

A disk that is missing from the list needs its controller's driver. A machine whose console shows
`addresses none yet` matched no network file, or its port has no driver.

## What next

- [Install a node](../operate/install.md) continues with the install.
- [Add software, drivers and kernel modules](add-software.md) adds the same drivers to the images
  the installed nodes run.
- [Sign images for Secure Boot](sign-images.md) covers the db key.
- [Architecture for developers](../contribute/architecture.md#an-install-runs-in-fixed-steps) explains
  what the installer does during an install.
