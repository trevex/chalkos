---
title: "Run on KVM, Proxmox or libvirt"
description: "Run chalkos nodes as virtual machines on a KVM hypervisor"
---

# Run on KVM, Proxmox or libvirt

chalkos nodes run as KVM virtual machines from the `kvm`
[platform](../reference/glossary.md#platform)'s images, with UEFI Secure Boot and a virtual TPM
like a physical machine. This guide builds a node's role image for `kvm`, signs it, turns it into
a VM disk, creates the VM under plain QEMU, libvirt or Proxmox, and installs the node in place:
the VM boots its own role image into [maintenance mode](../reference/glossary.md#maintenance-mode)
and [`chalkctl install`](../reference/cli/chalkctl_install.md) gives it its identity and secrets,
without an installer. The VM's disk is the node's system disk, so nothing else on the hypervisor
changes. For a local test cluster, [chalklab](../reference/glossary.md#chalklab) does all of this
for you; this guide is for hypervisors you run yourself.

## Before you begin

You need:

- A [cluster definition](../reference/glossary.md#cluster-definition) in a flake, its
  [secrets file](../reference/glossary.md#secrets-file), and
  [`chalkos.cluster.osCA`](../reference/options.md#chalkosclusterosca) set to
  `./secrets.pub.json`, as in [Install on bare metal](install-bare-metal.md#before-you-begin).
- A Secure Boot db key and certificate, `db.key` and `db.crt`, from
  [Sign images for Secure Boot](secure-boot-signing.md). On a VM you own the firmware's variable
  store, so you enrol the certificate yourself, as shown below.
- A KVM host with QEMU, OVMF built with Secure Boot and SMM support, and swtpm. libvirt and
  Proxmox bring all three. `qemu-img` on the machine where you prepare the disk.
- About 4 GiB of memory and 2 vCPUs per control plane and 2 GiB per worker, as the
  [requirements](../getting-started/requirements.md) give, and at least 16 GiB of disk per node.

## Declare the nodes on the kvm platform

A node's platform decides which image it runs. Every image carries the virtio drivers; the `kvm`
image also writes its console to the first serial port alone and runs the QEMU guest agent,
which the `metal` image lacks. Set
[`platform`](../reference/options.md#chalkosnodesplatform) on each VM node, and name the disk by
the device a virtio block disk gets, `/dev/vda`:

```nix title="nodes.nix"
{
  chalkos.nodes.cp1 = {
    role = "controlplane";
    platform = "kvm";
    storage.system.disk = "/dev/vda";
    network.networks."10-lan" = {
      matchConfig.MACAddress = "52:54:00:12:00:11";
      address = [ "10.0.0.11/24" ];
      gateway = [ "10.0.0.1" ];
    };
  };
}
```

Matching the network by MAC address keeps it independent of the interface name, and the VM is
given that MAC address below. On Proxmox with a SCSI disk instead of a virtio block disk, the
disk is `/dev/sda`. A node's platform is fixed at install; moving it to another one is a
reinstall.

## Build and sign the image

Build the role's image for `kvm` and sign a writable copy of it with
[`chalkctl sign`](../reference/cli/chalkctl_sign.md), so the VM's firmware boots it:

```sh
nix build .#chalkos.<cluster>.roles.controlplane.images.kvm
cp --sparse=always result/chalkos_0.1.0.raw controlplane.raw
chmod u+w controlplane.raw
chalkctl sign --image=controlplane.raw --repart-json=result/repart-output.json \
  --key=db.key --cert=db.crt
```

`<cluster>` is the flake output's name, and `0.1.0` the role's
[image version](../reference/glossary.md#image-version). The image holds the
[ESP](../reference/glossary.md#esp) and [slot](../reference/glossary.md#slot) A only, and every
node of the role boots the same signed image; what differs between nodes reaches them at
install. Sign before the first boot: an in-place install sends no image, so the image the VM
boots is the one the node keeps until its first [upgrade](../reference/glossary.md#upgrade).

## Make a disk for each VM

Give each VM its own disk, converted from the signed image and grown to the node's disk size.
On its first boot the image adds slot B and [STATE](../reference/glossary.md#state) behind slot
A, and the install creates [VAR](../reference/glossary.md#var) in the rest of the disk, so the
disk must be larger than the image:

```sh
qemu-img convert -f raw -O qcow2 controlplane.raw cp1.qcow2
qemu-img resize cp1.qcow2 32G
```

qcow2 stays thin and supports snapshots. Proxmox imports the raw image directly, so skip this
step there.

## What the VM needs

Every hypervisor below sets the same things:

| Setting | Value | Why |
| --- | --- | --- |
| Machine type | q35 with SMM | OVMF's Secure Boot build needs SMM to protect its variables. |
| Firmware | OVMF with Secure Boot on and `db.crt` in db | The firmware boots only images signed with a key it trusts. |
| TPM | TPM 2.0, emulated by swtpm | chalkos seals the disk keys to it; its state must persist with the VM. |
| Disk | virtio block, the image's disk | The node's system disk, `/dev/vda` in the guest. |
| Network | virtio-net, with the MAC address the node's network matches | |
| Serial port | the first one, `ttyS0` | The kvm image's console, where chalkd prints its fingerprint. |
| Guest agent channel | virtio-serial port `org.qemu.guest_agent.0` | Optional: lets the host shut the VM down cleanly and read its addresses. |

The guest agent answers only `guest-sync-delimited`, `guest-sync`, `guest-ping`, `guest-info`,
`guest-get-osinfo`, `guest-get-host-name`, `guest-get-time`, `guest-get-timezone`,
`guest-network-get-interfaces` and `guest-shutdown`. It never runs commands or reads or writes
files for the host, which the agent would otherwise allow, so the hypervisor's admin gets no shell
on the node through it.

Keep the VM's variable store and TPM state for the node's life. The node's disk keys are sealed to
[PCR 7](../reference/glossary.md#pcr-7), which measures the Secure Boot variables, so a reset
variable store or a new TPM state stops the node from unlocking without its
[recovery key](../reference/glossary.md#recovery-key).

### Plain QEMU

Prepare a variable store with Secure Boot on and your db certificate enrolled with
`virt-fw-vars` from virt-firmware. `--enroll-generate` creates a platform key and KEK for the
store, and `--no-microsoft` leaves Microsoft's certificates out, so the VM boots only what you
signed:

```sh
mkdir -p cp1/tpm
virt-fw-vars --input <ovmf-vars> --output cp1/OVMF_VARS.fd \
  --enroll-generate "cp1 platform key" --no-microsoft \
  --add-db "$(uuidgen)" db.crt --secure-boot
swtpm socket --tpm2 --tpmstate dir=cp1/tpm --ctrl type=unixio,path=cp1/tpm/swtpm.sock --daemon
```

`<ovmf-vars>` is the variable template of your OVMF build, and `<ovmf-code>` below its code: for
example `OVMF_VARS_4M.fd` and `OVMF_CODE_4M.secboot.fd` on Debian and Ubuntu, or
`OVMF_VARS.fd` and `OVMF_CODE.fd` of nixpkgs's `OVMFFull.fd`. The two must come from the same
build. Then start the VM:

```sh
qemu-system-x86_64 -name cp1 -machine q35,smm=on,accel=kvm -cpu host -m 4096 -smp 2 \
  -global driver=cfi.pflash01,property=secure,value=on \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=<ovmf-code> \
  -drive if=pflash,format=raw,unit=1,file=cp1/OVMF_VARS.fd \
  -drive if=none,id=disk0,format=qcow2,file=cp1.qcow2 \
  -device virtio-blk-pci,drive=disk0,bootindex=1 \
  -nic bridge,br=br0,model=virtio-net-pci,mac=52:54:00:12:00:11 \
  -chardev socket,id=chrtpm,path=cp1/tpm/swtpm.sock \
  -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-tis,tpmdev=tpm0 \
  -device virtio-serial-pci,id=agent \
  -chardev socket,id=qga,path=cp1/qga.sock,server=on,wait=off \
  -device virtserialport,bus=agent.0,chardev=qga,name=org.qemu.guest_agent.0 \
  -nographic
```

`-nographic` puts the serial console on your terminal. `-nic bridge` needs QEMU's bridge helper
to allow `br0`; use a tap device or another network backend if you prefer. chalklab starts its
VMs with the same devices, which makes `pkg/lab/vm.go` in the chalkos repository a working
reference.

### libvirt

Write the domain, with the firmware chosen by its features and a TPM emulated by swtpm, which
libvirt starts itself:

```xml title="cp1.xml"
<domain type="kvm">
  <name>cp1</name>
  <memory unit="MiB">4096</memory>
  <vcpu>2</vcpu>
  <os firmware="efi">
    <type machine="q35">hvm</type>
    <firmware>
      <feature enabled="yes" name="secure-boot"/>
      <feature enabled="yes" name="enrolled-keys"/>
    </firmware>
    <loader secure="yes"/>
  </os>
  <features>
    <acpi/>
    <smm state="on"/>
  </features>
  <cpu mode="host-passthrough"/>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="qcow2"/>
      <source file="/var/lib/libvirt/images/cp1.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
    <interface type="bridge">
      <source bridge="br0"/>
      <mac address="52:54:00:12:00:11"/>
      <model type="virtio"/>
    </interface>
    <tpm model="tpm-crb">
      <backend type="emulator" version="2.0"/>
    </tpm>
    <serial type="pty">
      <target type="isa-serial" port="0"/>
    </serial>
    <console type="pty">
      <target type="serial" port="0"/>
    </console>
    <channel type="unix">
      <target type="virtio" name="org.qemu.guest_agent.0"/>
    </channel>
  </devices>
</domain>
```

With `enrolled-keys`, libvirt copies a variable template that holds the distribution's and
Microsoft's certificates, but not yours. libvirt creates the VM's variable store when the VM
first starts, so start it paused, stop it, and add your certificate to db in that file:

```sh
virsh define cp1.xml
virsh start cp1 --paused
virsh destroy cp1
virsh dumpxml cp1 | grep nvram
sudo virt-fw-vars --inplace <nvram-file> --add-db "$(uuidgen)" db.crt
virsh start cp1
virsh console cp1
```

`<nvram-file>` is the path `virsh dumpxml` prints, such as
`/var/lib/libvirt/qemu/nvram/cp1_VARS.fd`. virt-fw-vars edits raw edk2 variable stores; where a
distribution keeps them as qcow2, enrol the certificate in the firmware's setup screen instead,
as described for Proxmox below.

### Proxmox

Copy the signed `controlplane.raw` to the Proxmox host and create the VM from it. Proxmox
imports the raw image into its storage, so no qcow2 is needed:

```sh
qm create 111 --name cp1 --ostype l26 --machine q35 --bios ovmf --cores 2 --memory 4096 \
  --efidisk0 local-lvm:1,efitype=4m,pre-enrolled-keys=1 \
  --tpmstate0 local-lvm:1,version=v2.0 \
  --virtio0 local-lvm:0,import-from=/root/controlplane.raw \
  --net0 virtio=52:54:00:12:00:11,bridge=vmbr0 \
  --serial0 socket --vga serial0 --agent enabled=1 --boot order=virtio0
qm disk resize 111 virtio0 32G
```

`111` is the VM ID and `local-lvm` the storage; use yours. `pre-enrolled-keys=1` turns Secure
Boot on with the distribution's and Microsoft's certificates enrolled. Your db certificate has to
be added in the firmware's setup screen, from a file on a FAT disk the VM can read:

```sh
openssl x509 -in db.crt -outform DER -out db.cer
mkfs.vfat -C db-cert.img 1440
mcopy -i db-cert.img db.cer ::db.cer
qm set 111 --virtio1 local-lvm:0,import-from=/root/db-cert.img
qm start 111
qm terminal 111
```

Press Esc while the firmware starts, then go to Device Manager, Secure Boot Configuration, set
Secure Boot Mode to Custom Mode, and open Custom Secure Boot Options, DB Options, Enroll
Signature, Enroll Signature Using File. Pick `db.cer` on the small disk, commit the change, leave
the setup and reset the VM. Detach and delete the small disk once the node runs. An alternative
is a variable store you prepare with `virt-fw-vars` from a 4m OVMF template and import with the
`import-from` option of `efidisk0`.

## Install the node in place

The VM boots its role image. On this first boot the image adds slot B and STATE to the disk,
finds no installed node and starts chalkd in maintenance mode, which prints its addresses and the
fingerprint of its certificate on the serial console:

```text
chalkd: maintenance mode, accepting clients of the OS CA; certificate fingerprint <fingerprint>
chalkd: addresses 10.0.0.57; certificate fingerprint <fingerprint>
```

Before it is installed, the node takes an address by DHCP. Install it at that address:

```sh
chalkctl install cp1 --endpoint=<address> --fingerprint=<fingerprint>
```

chalkctl prints `installing cp1 in place` and, once the node accepted everything,
`cp1 is installed and reboots`. It sends no image: the node keeps the image it runs, makes STATE
match the node's encryption policy, writes its identity and certificates there, creates VAR
and its volumes, enrols the second keyslot and gives the ESP a random partition UUID, so no two
VMs made from the same image share it. The node reboots into
[normal mode](../reference/glossary.md#normal-mode) with the static address of its identity.

From here a VM cluster continues as a bare-metal one:
[bootstrap the first control plane](install-bare-metal.md#bootstrap-the-first-control-plane),
then install the other nodes.

## Use the installer instead

The [installer](../reference/glossary.md#installer) works in a VM too: attach the signed
installer ISO from [Install on bare metal](install-bare-metal.md#build-the-installer) as a CD-ROM
next to an empty virtio disk, and install as on a physical machine. chalkctl then sends the
node's `kvm` image to the installer, signed with `--sign-key` and `--sign-cert`. This takes one
image download per node instead of one disk copy, which suits a hypervisor you cannot copy disk
images to.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the node's platform and whether
it matches the cluster definition, and the hypervisor sees the guest agent:

```sh
chalkctl status cp1
virsh domifaddr cp1 --source agent
```

On Proxmox, the VM's summary shows the addresses the agent reports.

## If something goes wrong

| Symptom | Cause and fix |
| --- | --- |
| The firmware reports `Access Denied` and boots nothing | The image is unsigned or signed with a key whose certificate is not in the VM's db. Sign the image, or enrol `db.crt`. |
| Nothing appears on the console after the firmware | The VM has no serial port, or the console shows the screen; the kvm image writes to `ttyS0` only. |
| `the identity places the system on /dev/sda, but the node runs from /dev/vda; install it with the installer instead` | `storage.system.disk` names another disk than the one the VM booted from. Fix the cluster definition. |
| `cp1 runs an image built for metal, but the cluster definition declares it on kvm; changing a node's platform is a reinstall` | The VM booted the `metal` image. Build and sign `images.kvm`. |
| The node asks for its recovery key after a hypervisor change | The variable store or the TPM state was reset or replaced. Enter `chalkctl recovery-key cp1`; see [Recover a node](recover-node.md). |

## What next

- [The cluster definition](../concepts/cluster-definition.md) explains platforms and how a
  role's image is built for each.
- [Sign images for Secure Boot](secure-boot-signing.md) covers the key's life and why it must
  not change for an installed node.
- [Install on bare metal](install-bare-metal.md) continues with bootstrap, kubeconfigs and
  recovery keys.
- [Upgrade a cluster](upgrade-cluster.md) rolls new images to the VMs with
  `chalkctl upgrade --sign-key=db.key --sign-cert=db.crt`.
