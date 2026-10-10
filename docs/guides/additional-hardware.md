---
title: "Support additional hardware"
description: "Add drivers, firmware and platform settings for hardware the base images do not cover"
---

# Support additional hardware

A chalkos image carries a chosen part of the kernel's modules, no firmware files and no CPU
microcode by default, which keeps the store that every install and upgrade sends small. When a
network card, storage controller or GPU is not found, add its driver and firmware to the role or
[platform](../reference/glossary.md#platform) whose nodes need it, build, and roll the new image
out. This guide finds the missing module, adds it in the right place, adds firmware and
out-of-tree modules, defines a platform of your own, and covers the size checks a larger image
meets.

## Before you begin

You need a [cluster definition](../reference/glossary.md#cluster-definition) with the role in question, and a way to see the hardware: a Linux
live system booted on the machine, or the vendor's specification of the card. For nodes that run
already, [Upgrade a cluster](upgrade-cluster.md) rolls the new image out; machines that are not
installed yet may need the same driver in the [installer](../reference/glossary.md#installer), which
[Customise the installer](customise-installer.md) adds.

## Find the module

On a live Linux system on the machine, `lspci -k` names each PCI device with the driver in use
and the modules that can drive it. For a wireless card of an edge node:

```console
$ lspci -k -s 01:00.0
01:00.0 Network controller: MEDIATEK Corp. MT7922 802.11ax PCI Express Wireless Network Adapter
	Subsystem: MEDIATEK Corp. Device e616
	Kernel driver in use: mt7921e
	Kernel modules: mt7921e
```

`lsusb -t` does the same for USB devices. `modinfo -n` prints where the module lives in the
kernel's tree, and that directory decides which
[module group](../reference/glossary.md#module-group) holds it:

```console
$ modinfo -n mt7921e
/lib/modules/6.18.55/kernel/drivers/net/wireless/mediatek/mt76/mt7921/mt7921e.ko.xz
```

`drivers/net/wireless` belongs to the `wireless` group. Then check whether the role's image
carries the module. The image's module tree is a build output of its own, so this needs no full
image build:

```console
$ nix build .#chalkos.<cluster>.roles.<role>.nixos.metal.config.system.build.chalkosKernelModules
$ find result/ -name 'mt7921e*'
```

No output means the image lacks the module; a module it carries prints its path, such as
`result/lib/modules/6.18.55/kernel/drivers/net/ethernet/mellanox/mlx5/core/mlx5_core.ko.xz`.
Replace `metal` with the node's platform.

## Add a module group or single modules

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
| `platform` | [TPM](../reference/glossary.md#tpm), IPMI, watchdogs, sensors, ACPI, CPU frequency, crypto, the console keyboard |

The further groups are `gpu`, `sound`, `media`, `wireless`, `infiniband`, `can` and
`industrial`. Add a group or single modules in a role's NixOS modules, which apply to all its
images:

```nix title="cluster.nix"
{
  chalkos.roles.gpu-worker = {
    kubernetes.kind = "worker";
    nixosModules = [
      {
        chalkos.kernel.moduleGroups = [ "gpu" ];
        chalkos.kernel.extraModules = [ "kvm_amd" ];
      }
    ];
  };
}
```

A role's groups add to the base groups; `lib.mkForce` replaces them, for an image that carries
less. [`chalkos.kernel.extraModules`](../reference/options.md#chalkoskernelextramodules) names
single modules, for a driver whose directory holds much you do not need; each comes with the
modules it depends on. [`chalkos.kernel.allModules`](../reference/options.md#chalkoskernelallmodules)
carries the kernel's whole tree, which adds about 100 MiB to the store and is meant for finding
out what a machine needs, not for production.

A module name the kernel does not know fails the build:

```text
error: unknown kernel module mlx5core, named by chalkos.kernel.extraModules or a module group
```

So does a module the image loads, through `boot.kernelModules` or the initrd, that its tree does
not hold, so a missing driver shows at build time, not at the machine.

## Load drivers early for the system disk

The initrd finds the store, [STATE](../reference/glossary.md#state) and the node's other disks before the system starts, so the
driver of every disk controller a node's storage uses must be in the initrd, not only in the
image. The initrd carries NixOS's common disk drivers, such as `ahci`, `nvme`, `sd_mod` and
`mmc_block`. A system disk or an extra disk behind another controller, a hardware RAID adapter for
example, needs its driver added:

```nix
{
  boot.initrd.availableKernelModules = [ "megaraid_sas" ];
}
```

The module must also be in the image's tree, which the build checks; `megaraid_sas` is in the
`storage` group.

## Add firmware and microcode

Many network, storage and GPU drivers load firmware files at runtime, and the images carry none.
NixOS's [`hardware.firmware`](https://nixos.org/manual/nixos/stable/options#opt-hardware.firmware)
adds packages of them. `pkgs.linux-firmware` holds every vendor's files and takes hundreds of MiB
of the store, which every upgrade sends, so take only the files a driver needs. `modinfo -F
firmware` lists the files a module asks for:

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

The driver serves several chips; the MT7922 above needs the two files that name it. A role takes
the group and those files:

```nix title="cluster.nix"
{
  chalkos.roles.edge.nixosModules = [
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
}
```

`hardware.cpu.intel.updateMicrocode` and `hardware.cpu.amd.updateMicrocode` put the CPU vendor's
microcode at the start of the initrd, which the [UKI](../reference/glossary.md#uki) carries, so
the kernel loads it at its earliest point on every boot.

## Add an out-of-tree module

A module that is not part of the kernel, from a nixpkgs kernel package or one you build, goes
into NixOS's `boot.extraModulePackages`. It is carried whole, together with the kernel's modules
it depends on, whatever the groups say:

```nix
{ config, ... }:
{
  boot.extraModulePackages = [ config.boot.kernelPackages.v4l2loopback ];
  boot.kernelModules = [ "v4l2loopback" ];
}
```

The package must be built for the image's kernel, which `config.boot.kernelPackages` ensures.
The build fails when such a module depends on one the kernel does not have.

## Define a platform of your own

A role applies to every node of that role, wherever it runs. Settings that belong to a kind of
machine instead, such as the drivers and firmware of one server model, its serial console or a
vendor's agent, belong in a platform. chalkos defines `metal` and `kvm`;
[`chalkos.platforms`](../reference/options.md#chalkosplatforms) adds to them or defines another:

```nix title="cluster.nix"
{
  chalkos.platforms.r650.nixosModules = [
    {
      boot.kernelParams = [
        "console=tty0"
        "console=ttyS1,115200"
      ];
      chalkos.kernel.extraModules = [ "mlx5_ib" ];
    }
  ];

  chalkos.nodes.w3 = {
    role = "worker";
    platform = "r650";
    storage.system.disk = { model = "Dell BOSS-N1"; };
  };
}
```

Each role is built once per platform, as `chalkos.roles.<role>.images.<platform>`, and only for
the platforms its nodes use when chalkctl builds them, so only the images of `r650` carry its
drivers. A platform of your own starts from chalkos's node modules alone: it does not include
`metal`'s modules, so give it a console. Its name, lower-case letters, digits and dashes, becomes
`CHALKOS_PLATFORM` in the image's os-release, and a node refuses images and [identities](../reference/glossary.md#identity) of another
platform, so moving a node to a new platform is a reinstall. Platform modules come before the
role's; a role overrides a value its platform sets with `lib.mkForce`.

## Keep the image within its slot

A larger module tree or firmware grows the [store](../reference/glossary.md#store). Each image
build checks that the store's data takes at most 80% of a [slot](../reference/glossary.md#slot), 3 GiB by default, its hash tree
at most 80% of the hash partition, and the UKIs the [ESP](../reference/glossary.md#esp) holds at most 80% of the ESP, so the next
image still fits. A role that grows past that fails the build:

```text
error: worker: the store's data takes 2671771648 bytes, more than 80% of its slot of 3221225472 (chalkos.disk.storeSize)
```

Leave out what the role does not need first. Raising
[`chalkos.disk.storeSize`](../reference/options.md#chalkosdiskstoresize) helps only nodes installed
afterwards: a node's partitions are laid out at install, and an upgrade whose store does not fit
the node's slot is refused. Every MiB of the store is also sent to each node at every upgrade.

## Check that it worked

After the upgrade or install, the device works when its driver bound to it. Nodes have no logins;
with [`chalkos.debug.tools`](../reference/options.md#chalkosdebugtools) on the role, a privileged
pod reaches the node's tools:

```sh
kubectl debug node/<node> -it --image=busybox -- chroot /host /run/current-system/sw/bin/bash
```

There, `ip link` lists a new network card, `lsblk` a new disk, and `dmesg` names the firmware
file a driver failed to load. A storage controller missing from the initrd shows earlier: the
node does not find its disk and stops in the initrd, which the console shows.

## What next

- [The image](../concepts/image.md) explains the module tree, the store and the slots.
- [Customise a role](customise-role.md) covers the rest of what a role's modules may change.
- [Customise the installer](customise-installer.md) adds the same drivers to the installer.
- [Upgrade a cluster](upgrade-cluster.md) rolls the new image out.
