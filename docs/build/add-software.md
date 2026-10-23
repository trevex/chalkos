---
title: "Add software, drivers and kernel modules"
description: "Add packages, services, kernel modules, firmware and debug tools to a cluster's images within an appliance's limits"
---

# Add software, drivers and kernel modules

This page is for an image author who adds a service, a driver or firmware to a cluster's images.
At the end the images carry what you added, the build has checked that every kernel module they
load is present, and the images still fit their slot. It starts from the flake of
[Your first image flake](first-image.md).

A chalkos image carries a chosen part of the kernel's modules, no firmware files and no CPU
microcode by default. That keeps the store small, and every install and upgrade sends the whole
store to the node.

## Before you begin

You need the flake that builds the cluster's images with `chalkos.lib.mkImages`. To find a missing
driver you need a way to see the hardware: a Linux live system booted on the machine, or the
vendor's specification of the card.

## A module adds a service to every variant

A module of the images is an ordinary NixOS module. This one runs a service on every node, with
the tool it calls in its own `path`:

```nix title="modules/disk-report.nix"
{ pkgs, ... }:
{
  systemd.services.disk-report = {
    wantedBy = [ "multi-user.target" ];
    path = [ pkgs.smartmontools ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${pkgs.smartmontools}/bin/smartctl --scan";
    };
  };
}
```

Add it to `modules` in `flake.nix`. Every variant's images carry `modules`, after their
platform's modules and before the variant's own:

```nix
images = chalkos.lib.mkImages {
  platforms = [ "metal" ];
  modules = [ ./modules/disk-report.nix ];
  variants.node.modules = [ ./extensions/rack.nix ];
};
```

A module that only some nodes need goes into one variant's `modules` instead, so the other nodes'
images stay without it. A module that belongs to a kind of machine goes into a platform.
[Variants and platforms](variants-and-platforms.md) covers both and the order the modules apply in.

Nodes have no logins, so the system path holds none of NixOS's default packages. It holds what the
image's own modules put there, such as systemd, bash, less, nftables, kmod and the mount helpers,
and what your modules add. Name the tools a unit runs in its `path`, as above.
`environment.systemPackages` works as on any NixOS system. It is the place for what other programs
look up on the system path, such as a mount helper (`mount.nfs`) that a CSI driver runs.

## Find the kernel module a device needs

On a live Linux system on the machine, `lspci -k` names each PCI device with the driver in use and
the modules that can drive it. For a wireless card of an edge node:

```console
$ lspci -k -s 01:00.0
01:00.0 Network controller: MEDIATEK Corp. MT7922 802.11ax PCI Express Wireless Network Adapter
	Subsystem: MEDIATEK Corp. Device e616
	Kernel driver in use: mt7921e
	Kernel modules: mt7921e
```

`lsusb -t` does the same for USB devices. `modinfo -n` prints where the module lives in the
kernel's tree. That directory decides which module group holds it:

```console
$ modinfo -n mt7921e
/lib/modules/6.18.55/kernel/drivers/net/wireless/mediatek/mt76/mt7921/mt7921e.ko.xz
```

`drivers/net/wireless` belongs to the `wireless` group. Next, check whether the image carries the
module. The image's module tree is a build output of its own, so this needs no full image build.
Expose the tree of the image's NixOS system in `flake.nix`, next to the images:

```nix
packages.x86_64-linux.node-metal-modules =
  images.nixos.node-metal.config.system.build.chalkosKernelModules;
```

Then build it and look for the module:

```console
$ nix build .#node-metal-modules
$ find result/ -name 'mt7921e*'
```

No output means the image lacks the module. A module the image carries prints its path, such as
`result/lib/modules/6.18.55/kernel/drivers/net/ethernet/mellanox/mlx5/core/mlx5_core.ko.xz`.
Replace `node-metal` with the variant and platform of the node's image.

## Kernel module groups select whole driver directories

[`chalkos.kernel.moduleGroups`](../reference/options.md#chalkoskernelmodulegroups) selects whole
directories of the kernel's tree, so a group follows a kernel update with its subsystem's new
drivers. Every image carries the base groups:

| Group | Holds |
| --- | --- |
| `storage` | ATA, NVMe, SCSI and SAS controllers, MD RAID, USB storage, MMC |
| `network` | wired Ethernet, PHYs, bonding, VLANs, bridges, WireGuard, VXLAN |
| `virtualisation` | virtio, Hyper-V, Xen, VMware, AWS ENA and Google gVNIC |
| `filesystems` | ext4, xfs, btrfs, FAT, erofs, overlayfs, NFS, CephFS, SMB |
| `kubernetes` | netfilter, traffic control, SCTP, Open vSwitch |
| `platform` | TPM, IPMI, watchdogs, sensors, ACPI, CPU frequency, crypto, the console keyboard |

The base groups keep about a third of the kernel's 7,300 modules, about 45 MiB of the full tree's
145 MiB. The further groups are `gpu`, `sound`, `media`, `wireless`, `infiniband`, `can` and
`industrial`. Add a group or single modules in the modules of the variant whose nodes need them,
here a `gpu` variant:

```nix
variants.gpu.modules = [
  {
    chalkos.kernel.moduleGroups = [ "gpu" ];
    chalkos.kernel.extraModules = [ "kvm_amd" ];
  }
];
```

Every image of the variant carries them. In `modules`, they go to every variant. The groups you
set add to the base groups. `lib.mkForce` replaces the base groups, for an image that carries
less.

[`chalkos.kernel.extraModules`](../reference/options.md#chalkoskernelextramodules) names single
modules, for a driver whose directory holds much you do not need. Each comes with the modules it
depends on. [`chalkos.kernel.allModules`](../reference/options.md#chalkoskernelallmodules)
carries the kernel's whole tree. It adds about 100 MiB to the store and is meant for finding out
what a machine needs, not for production.

A missing module fails the build, not the boot. A module name the kernel does not know fails it:

```text
error: unknown kernel module mlx5core, named by chalkos.kernel.extraModules or a module group
```

So does a module the image loads, through `boot.kernelModules` or the initrd, that its tree does
not hold, and a dependency of a module in the tree that the tree lacks.

## The initrd needs the system disk's driver

The initrd finds the store, STATE and the node's other disks before the system starts. So the
driver of every disk controller a node's storage uses must be in the initrd, not only in the
image. The initrd carries NixOS's common disk drivers, such as `ahci`, `nvme`, `sd_mod` and
`mmc_block`. A system disk or an extra disk behind another controller, such as a hardware RAID
adapter, needs its driver added:

```nix
{
  boot.initrd.availableKernelModules = [ "megaraid_sas" ];
}
```

The module must also be in the image's tree, which the build checks. `megaraid_sas` is in the
`storage` group.

## Firmware and microcode come from hardware.firmware

Many network, storage and GPU drivers load firmware files at runtime, and the images carry none.
NixOS's [`hardware.firmware`](https://nixos.org/manual/nixos/stable/options#opt-hardware.firmware)
adds packages of them. `pkgs.linux-firmware` holds every vendor's files and takes hundreds of MiB
of the store, which every upgrade sends. Take only the files a driver needs. `modinfo -F firmware`
lists the files a module asks for:

```console
$ modinfo -F firmware mt7921e
mediatek/WIFI_MT7902_patch_mcu_1_1_hdr.bin
mediatek/WIFI_RAM_CODE_MT7902_1.bin
mediatek/WIFI_MT7922_patch_mcu_1_1_hdr.bin
mediatek/WIFI_RAM_CODE_MT7922_1.bin
mediatek/WIFI_MT7961_patch_mcu_1_2_hdr.bin
mediatek/WIFI_RAM_CODE_MT7961_1.bin
mediatek/WIFI_MT7961_patch_mcu_1a_2_hdr.bin
mediatek/WIFI_RAM_CODE_MT7961_1a.bin
```

The driver serves several chips. The MT7922 above needs the two files that name it. A variant
`edge`, for the machines that have the card, takes the group and those files:

```nix
variants.edge.modules = [
  (
    { pkgs, ... }:
    {
      chalkos.kernel.moduleGroups = [ "wireless" ];
      hardware.firmware = [
        (pkgs.runCommand "mt7922-firmware" { } ''
          mkdir -p $out/lib/firmware/mediatek
          cp ${pkgs.linux-firmware}/lib/firmware/mediatek/WIFI_{MT7922_patch_mcu_1_1_hdr,RAM_CODE_MT7922_1}.bin \
            $out/lib/firmware/mediatek/
        '')
      ];
      hardware.cpu.amd.updateMicrocode = true;
    }
  )
];
```

`hardware.cpu.intel.updateMicrocode` and `hardware.cpu.amd.updateMicrocode` put the CPU vendor's
microcode at the start of the initrd. The UKI carries the initrd, so the kernel loads the
microcode at its earliest point on every boot.

## Out-of-tree modules come whole

A module that is not part of the kernel, from a nixpkgs kernel package or one you build, goes into
NixOS's `boot.extraModulePackages`. It is carried whole, with the kernel's modules it depends on,
whatever the groups say:

```nix
{ config, ... }:
{
  boot.extraModulePackages = [ config.boot.kernelPackages.v4l2loopback ];
  boot.kernelModules = [ "v4l2loopback" ];
}
```

The package must be built for the image's kernel, which `config.boot.kernelPackages` ensures. The
build fails when such a module depends on one the kernel does not have.

## Debug tools are an option of the image

[`chalkos.debug.tools`](../reference/options.md#chalkosdebugtools) adds coreutils, grep, sed,
findutils, procps, iproute2 and util-linux to the system path, and crictl on images with
Kubernetes:

```nix
{
  chalkos.debug.tools = true;
}
```

The option changes the image, so it reaches running nodes with an upgrade.

!!! note "Kubernetes: a shell through a privileged pod"

    Nodes have no logins and no SSH. On a node with Kubernetes, a privileged pod reaches the
    tools through the host's root, as
    [Troubleshooting](../operate/troubleshooting.md#open-a-shell-on-a-node) shows. A node without
    Kubernetes has `chalkctl logs` and its console.

## The image is an appliance

An image is built once and never changed on the node:

- There is no Nix daemon, no `nixos-rebuild` and no `switch-to-configuration` on the node.
- The root file system is a tmpfs, so nothing written outside STATE and VAR survives a reboot.
- `/etc` is assembled from the image at boot and stays read-only.
- The store is read-only and checked by dm-verity.
- Users and groups are fixed at build time. Nodes have no interactive logins, so no account needs
  a password or an SSH key.

A service keeps its data below `/var`, which is VAR, or on a volume. Within that, a module can set
anything, and these settings break the node or are refused:

- chalkd, `chalkos-config.service`, `chalkos-health.service` and systemd-networkd install,
  configure and upgrade the node. An image that disables or replaces them builds, and the node
  then cannot be installed or upgraded.
- The outputs `networkd` and `chrony` render the node's network and time servers. They own
  `/run/systemd/network` and `/run/chalkos/outputs/chrony`, so your outputs take other names and
  directories. The evaluation refuses a directory that two outputs share.
- `mkImages` sets `chalkos.variant.name`, `chalkos.platform.name`, `chalkos.kubernetes.enable`,
  `chalkos.maintenance.osCA` and `chalkos.provenance`. Set the variant's and platform's names, the
  variant's `kubernetes` and the arguments `osCA`, `revision` and `tag` instead.
- The partition sizes under `chalkos.disk` are laid out when a node is installed. A changed size
  applies only to nodes installed afterwards.
- On a variant without Kubernetes, a boot is healthy only when no unit that `multi-user.target` or
  `sysinit.target` pulls in failed. A unit of yours that fails makes the next upgrade roll back.
  Jobs that timers and sockets start never count, and
  [`chalkos.upgrade.healthIgnoreUnits`](../reference/options.md#chalkosupgradehealthignoreunits)
  lists units whose failure does not count.

Your own image options take a namespace of their own, such as `rack.*` or `chalkos.rack.*`. Under
`chalkos`, these names are taken: `cni`, `debug`, `disk`, `effects`, `facts`, `kernel`, `kinds`,
`kubernetes`, `maintenance`, `outputs`, `platform`, `provenance`, `upgrade` and `variant`.

## Keep the image within its slot

Every package, module, driver and firmware file adds to the store, which each upgrade sends to
every node. Each image build checks that the next image will fit beside the running one. The build
fails when:

- the store's data takes more than 80% of its slot,
  [`chalkos.disk.storeSize`](../reference/options.md#chalkosdiskstoresize), 3 GiB by default;
- the hash tree takes more than 80% of its partition, `chalkos.disk.storeVeritySize`, 128 MiB by
  default;
- three UKIs, both slots' and an upgrade's temporary copy, take more than 80% of the ESP,
  `chalkos.disk.espSize`, 1 GiB by default.

The message names the variant:

```text
error: node: the store's data takes 2671771648 bytes, more than 80% of its slot of 3221225472 (chalkos.disk.storeSize)
```

Leave out what the variant does not need first. Raising `chalkos.disk.storeSize` helps only nodes
installed afterwards: a node's partitions are laid out at install, and a node refuses an upgrade
whose store does not fit its slot.

## Check that it worked

Build the image and check the nodes' config against it, as in
[Your first image flake](first-image.md#build-the-image). Then roll it out with
[Upgrade a cluster](../operate/upgrade-cluster.md), or install new machines with it. Machines
that are not installed yet may need the same driver in the installer, which
[Customise the installer](customise-installer.md) adds.

On the node, `chalkctl status` names failed units, and `chalkctl logs` shows a unit's journal:

```sh
chalkctl status w1
chalkctl logs w1 --unit=disk-report.service
```

A device works when its driver bound to it. With the debug tools, `ip link` lists a new network
card, `lsblk` a new disk, and `dmesg` names a firmware file a driver failed to load. A storage
controller missing from the initrd shows earlier: the node does not find its disk and stops in the
initrd, which the console shows.

## What next

- [Variants and platforms](variants-and-platforms.md) puts a driver on the nodes that need it.
- [The image](../how-it-works/node/image.md) explains the module tree, the store and the slots.
- [Customise the installer](customise-installer.md) adds the same drivers to the installer.
