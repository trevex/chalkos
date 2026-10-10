---
title: "Customise the installer"
description: "Give the installer static addresses, VLANs, bonds, drivers or firmware"
---

# Customise the installer

The cluster's [installer](../reference/glossary.md#installer) takes its addresses by DHCP on
every Ethernet port and carries the drivers of the base [module groups](../reference/glossary.md#module-group).
When a machine has no DHCP on its network, sits on a VLAN, needs a bond, or has a network card
or storage controller the installer cannot drive, add NixOS modules to the installer with
[`chalkos.installer.nixosModules`](../reference/options.md#chalkosinstallernixosmodules), build
it again and sign it. The change affects the installer alone; the role images and installed nodes
stay as they are.

The settings go into the image because under [Secure Boot](../reference/glossary.md#secure-boot)
the kernel command line is part of the signed [UKI](../reference/glossary.md#uki), so you cannot
add `ip=` or a driver option at the boot menu, and there is nothing on the medium to edit after
signing. Whatever differs between
machines goes into the installer and is matched at boot by MAC address or interface name. One
installer then serves the whole cluster: each machine finds the network configuration written for
it, and the others fall back to DHCP.

## Before you begin

You need the [cluster definition](../reference/glossary.md#cluster-definition) with
[`chalkos.cluster.osCA`](../reference/options.md#chalkosclusterosca) set, the [db](../reference/glossary.md#db-and-dbx) key and
certificate from [Sign images for Secure Boot](secure-boot-signing.md), and the MAC addresses of
the machines' ports, from the BMC, the firmware setup or the machines' labels.
[Install on bare metal](install-bare-metal.md) builds the unchanged installer.

## Give a machine a static address

The installer configures its network with systemd-networkd. Its default network file,
`99-ethernet-default-dhcp`, runs DHCP on every Ethernet port; networkd applies the first file in
name order that matches a port, so a file with a lower number takes the port over. Write one per
machine, matched by the MAC address of its port:

```nix title="installer.nix"
{
  chalkos.installer.nixosModules = [
    {
      systemd.network.networks."10-cp1" = {
        matchConfig.MACAddress = "3c:ec:ef:00:00:11";
        address = [ "10.0.0.11/24" ];
        gateway = [ "10.0.0.1" ];
        dns = [ "10.0.0.1" ];
      };
    }
  ];
}
```

Import the file from the cluster definition, next to your other modules. The options are NixOS's
[`systemd.network`](https://nixos.org/manual/nixos/stable/options#opt-systemd.network.networks)
options, the same shape as a node's `network`. Giving a machine the address its node will have
after the install lets chalkctl reach it without `--endpoint`, at the node's first static
address.

## Put the installer on a VLAN

A VLAN is a network device of its own, declared as a netdev and attached to the port that
carries it:

```nix title="installer.nix"
{
  chalkos.installer.nixosModules = [
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
  ];
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
  chalkos.installer.nixosModules = [
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
  ];
}
```

`802.3ad` needs LACP on the switch ports. Without it, use `active-backup`, which needs nothing
from the switch. A VLAN on the bond goes into the bond's file as `vlan = [ "vlan42" ];`, as above.

## Add drivers and firmware

The installer carries the base module groups, `storage`, `network`, `virtualisation`,
`filesystems`, `kubernetes` and `platform`, which hold the in-tree Ethernet, NVMe, SATA, SAS and
virtio drivers. Add a group or single modules, and firmware files, the way a role adds them:

```nix title="installer.nix"
{
  chalkos.installer.nixosModules = [
    (
      { pkgs, ... }:
      {
        chalkos.kernel.moduleGroups = [ "infiniband" ];
        chalkos.kernel.extraModules = [ "kvm_amd" ];
        hardware.firmware = [ pkgs.linux-firmware ];
      }
    )
  ];
}
```

The build fails on a module name the kernel does not have, so a typo shows at build time, not at
the machine. `linux-firmware` holds the firmware of every vendor and adds several hundred MiB;
[Support additional hardware](additional-hardware.md) shows how to find the right module and take
only the firmware files a card needs. The installer only needs to reach the network and see the
target disk. The node's role image needs the same drivers, set in its role or platform.

## Change the console

The installer writes its console to the screen and to the first serial port, `ttyS0`. Linux
sends kernel messages to every console on the command line, but [chalkd](../reference/glossary.md#chalkd)'s lines, with the
fingerprint and the addresses, go to the last one only. A machine whose BMC shows serial-over-LAN
on the second port needs that port last:

```nix title="installer.nix"
{
  chalkos.installer.nixosModules = [
    (
      { lib, ... }:
      {
        boot.kernelParams = lib.mkAfter [ "console=ttyS1,115200" ];
      }
    )
  ];
}
```

`lib.mkAfter` places the entry after the installer's own `console=` entries. A plain list would
come before them, leaving `ttyS0` last.

## What the installer must keep

The modules may change anything a NixOS module can, except three things that make the image the
installer. The build fails with a message when a module disables `chalkd.service`, changes its
`CHALKD_INSTALLER` environment variable, or sets `chalkos.platform.name`: the installer runs
chalkd in installer mode and installs images of every platform.

## Build and sign the installer

Build and sign the installer as in [Install on bare metal](install-bare-metal.md#build-the-installer):

```sh
nix build .#chalkos.<cluster>.installer.image
cp --sparse=always result/chalkos-installer_0.1.0.iso installer.iso
chmod u+w installer.iso
chalkctl sign --image=installer.iso --repart-json=result/chalkos-installer_0.1.0.iso.json \
  --key=db.key --cert=db.crt
```

Sign it with the key the role images are signed with: the installer seals each node's [STATE](../reference/glossary.md#state) to
[PCR 7](../reference/glossary.md#pcr-7) as measured under the installer, and a role image signed
with another key asks for the [recovery key](../reference/glossary.md#recovery-key) on its first boot.

The installer also carries the cluster's [OS CA](../reference/glossary.md#os-ca), from the
`secrets.pub.json` that `osCA` names, and accepts only clients whose certificate chains to it.
After an OS CA [rotation](../reference/glossary.md#rotation) with [`chalkctl rotate`](../reference/cli/chalkctl_rotate.md), build and
sign the installer again. An installer built before the rotation refuses chalkctl's new
certificate, and chalkctl says so: `the maintenance image trusts another OS CA than the secrets
file, so it refuses chalkctl's certificate`.

## Check that it worked

Boot a machine from the new installer. Its console shows the address you gave it, or the one
DHCP gave the VLAN or the bond, with the fingerprint:

```text
chalkd: addresses 10.0.0.11; certificate fingerprint <fingerprint>
```

[`chalkctl disks`](../reference/cli/chalkctl_disks.md) lists the machine's disks, which shows the
installer reaches the network and drives the storage controller:

```sh
chalkctl disks --endpoint=10.0.0.11 --fingerprint=<fingerprint> --secrets=secrets.age
```

A disk that is missing from the list needs its controller's driver; a machine whose console shows
`addresses none yet` matched no network file, or its port has no driver.

## What next

- [Install on bare metal](install-bare-metal.md) continues with the install.
- [Support additional hardware](additional-hardware.md) adds the same drivers to the role
  images, which the installed nodes run.
- [Sign images for Secure Boot](secure-boot-signing.md) covers the db key.
- [Architecture](../concepts/architecture.md#how-does-a-machine-become-a-node) explains what
  the installer does during an install.
