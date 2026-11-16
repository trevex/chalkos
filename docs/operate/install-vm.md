---
title: "Install a node in a VM"
description: "Run a node of the inventory as a VM under QEMU, libvirt or Proxmox, from the kvm image, and install it in place"
---

# Install a node in a VM

This page is for an operator who runs a node as a virtual machine under QEMU, libvirt or Proxmox.
The VM boots the node's own image from its disk, and `chalkctl install` installs it in place. At
the end the node runs its image with its config, and chalkctl reaches it at the address the
inventory names. The examples install a VM `vm1` on a bridge with the IPv4 network
`192.168.122.0/24`.

In short:

```sh
nix build .#node-kvm -o images/node-kvm
chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-kvm images/node-kvm-signed
cp --sparse=always images/node-kvm-signed/chalkos.raw node.raw
chalkctl install vm1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
chalkctl status vm1 --secrets=secrets.age
```

## Before you begin

You need the workstation, the inventory and the secrets file that
[Install a node](install.md#before-you-begin) lists, and Secure Boot keys with `keys/db.key`,
`keys/db.crt` and chalkbuild.

A VM needs a KVM host with QEMU, OVMF built with Secure Boot and SMM support, and swtpm. libvirt
and Proxmox bring all three. You need `qemu-img` where you prepare the VM's disk. Give each
control plane 2 vCPUs and about 3 GiB of memory, each worker 2 GiB, and every node at least
16 GiB of disk. chalklab gives its VMs these sizes.

## Build and sign the kvm image

A VM runs the `kvm` platform's image, which writes its console to the first serial port alone and
runs the QEMU guest agent. The image author adds `kvm` to the flake's platforms and exposes its
image as a package, such as `node-kvm`, as
[Images and platforms](../build/images-and-platforms.md#a-platform-builds-an-image-per-kind-of-machine)
describes.

Name the signed copy of the `kvm` image in the inventory's `spec.images`. `spec.all.imageName`
gives it to every node that names no image of its own or through a group:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: vms}
spec:
  images:
    # chalkbuild sign signs a copy of the build into this directory.
    node-kvm: {source: {directory: images/node-kvm-signed}}
  all: {imageName: node-kvm, files: [cluster/*.yaml]}
  nodes:
    vm1: {address: 192.168.122.11, files: [nodes/vm1.yaml]}
```

A VM's system disk is a virtio block disk, `/dev/vda` in the guest. Match the network by MAC
address, so that it does not depend on the interface's name, and give the VM that MAC address.
`vm1`'s `SystemDisk` and `Network` then read:

```yaml title="nodes/vm1.yaml"
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/vda}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {macAddress: "52:54:00:12:00:11"}
  network: {address: [192.168.122.11/24], gateway: 192.168.122.1}
```

On Proxmox with a SCSI disk in place of a virtio block disk, the disk is `/dev/sda`. A node's
platform is fixed at install. Moving a node to another platform takes a reinstall.

Sign the `kvm` image before the VM's first boot. An install in place sends no image, so the VM
keeps the image it boots until its first upgrade. A VM writes to its disk, so take a writable copy
of the signed raw image for the disks:

```sh
nix build .#node-kvm -o images/node-kvm
rm -rf images/node-kvm-signed
chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-kvm images/node-kvm-signed
chalkctl validate
cp --sparse=always images/node-kvm-signed/chalkos.raw node.raw
```

Every node whose image is `node-kvm` boots the same signed image, and what differs between nodes
reaches them at install. The image holds the ESP and slot A. Its first boot adds slot B and STATE,
and the install creates VAR in the rest of the disk, so the disk must be larger than the image.

## Create the VM

Every hypervisor below gives the VM the same settings:

| Setting | Value | Why |
| --- | --- | --- |
| Machine type | q35 with SMM | OVMF's Secure Boot build needs SMM to protect its variables. |
| Firmware | OVMF with Secure Boot on and `db.crt` in db | The firmware boots only images signed with a key it trusts. |
| TPM | TPM 2.0, emulated by swtpm | chalkos seals the disk keys to it, so its state must persist with the VM. |
| Disk | virtio block, made from `node.raw` | The node's system disk, `/dev/vda` in the guest. |
| Network | virtio-net, with the MAC address the node's `Network` matches | chalkctl reaches the node at the address of that network. |
| Serial port | the first one, `ttyS0` | The `kvm` image's console, where chalkd prints its fingerprint. |
| Guest agent channel | virtio-serial port `org.qemu.guest_agent.0` | Optional. The host can then shut the VM down cleanly and read its addresses. |

The guest agent runs no commands for the host, as
[Can anyone log in to a node?](../how-it-works/node/security.md#nobody-logs-in-to-a-node)
describes.

!!! warning "Keep the VM's UEFI variables and TPM state for the node's life"

    The node's disk keys are sealed to PCR 7, which measures the Secure Boot variables. A reset
    UEFI variables file or a new TPM state stops the node from unlocking without its recovery
    key.

Pick the tab for your hypervisor:

=== "QEMU"

    Give each VM its own disk, converted from the signed image and grown to the node's disk size.
    qcow2 stays thin and supports snapshots:

    ```sh
    qemu-img convert -f raw -O qcow2 node.raw vm1.qcow2
    qemu-img resize vm1.qcow2 32G
    ```

    Prepare a UEFI variables file with Secure Boot on and your db certificate enrolled, with
    `virt-fw-vars` from virt-firmware. `--enroll-generate` creates a platform key and KEK for the
    file. `--no-microsoft` leaves Microsoft's certificates out of db, and `--microsoft-kek none`
    leaves them out of KEK. The VM then boots only what you signed and accepts no db update that
    Microsoft signed. Then start the VM's TPM:

    ```sh
    mkdir -p vm1/tpm
    virt-fw-vars --input <ovmf-vars> --output vm1/OVMF_VARS.fd \
      --enroll-generate "vm1 platform key" --no-microsoft --microsoft-kek none \
      --add-db "$(uuidgen)" keys/db.crt --secure-boot
    swtpm socket --tpm2 --tpmstate dir=vm1/tpm --ctrl type=unixio,path=vm1/tpm/swtpm.sock --daemon
    ```

    `<ovmf-vars>` is the variables template of your OVMF build, and `<ovmf-code>` below is its
    code. On Debian and Ubuntu they are `OVMF_VARS_4M.fd` and `OVMF_CODE_4M.secboot.fd`. In
    nixpkgs they are `OVMF_VARS.fd` and `OVMF_CODE.fd` of `OVMFFull.fd`. The two must come from
    the same build. Then start the VM:

    ```sh
    qemu-system-x86_64 -name vm1 -machine q35,smm=on,accel=kvm -cpu host -m 3072 -smp 2 \
      -global driver=cfi.pflash01,property=secure,value=on \
      -drive if=pflash,format=raw,unit=0,readonly=on,file=<ovmf-code> \
      -drive if=pflash,format=raw,unit=1,file=vm1/OVMF_VARS.fd \
      -drive if=none,id=disk0,format=qcow2,file=vm1.qcow2 \
      -device virtio-blk-pci,drive=disk0,bootindex=1 \
      -nic bridge,br=br0,model=virtio-net-pci,mac=52:54:00:12:00:11 \
      -chardev socket,id=chrtpm,path=vm1/tpm/swtpm.sock \
      -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-tis,tpmdev=tpm0 \
      -device virtio-serial-pci,id=agent \
      -chardev socket,id=qga,path=vm1/qga.sock,server=on,wait=off \
      -device virtserialport,bus=agent.0,chardev=qga,name=org.qemu.guest_agent.0 \
      -nographic
    ```

    `-nographic` puts the serial console on your terminal. `-nic bridge` needs QEMU's bridge
    helper to allow `br0`. A tap device or another network backend works as well. chalklab starts
    its VMs with the same devices, so `pkg/lab/vm.go` in the chalkos repository is a working
    reference.

=== "libvirt"

    Give each VM its own disk, converted from the signed image and grown to the node's disk size:

    ```sh
    qemu-img convert -f raw -O qcow2 node.raw /var/lib/libvirt/images/vm1.qcow2
    qemu-img resize /var/lib/libvirt/images/vm1.qcow2 32G
    ```

    Write the domain. libvirt chooses the firmware by its features, and starts swtpm for the TPM
    itself:

    ```xml title="vm1.xml"
    <domain type="kvm">
      <name>vm1</name>
      <memory unit="MiB">3072</memory>
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
          <source file="/var/lib/libvirt/images/vm1.qcow2"/>
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

    With `enrolled-keys`, libvirt copies a variables template that holds the distribution's and
    Microsoft's certificates, but not yours. libvirt creates the VM's UEFI variables file when
    the VM first starts. Start the VM paused, stop it, and add your certificate to db in that
    file:

    ```sh
    virsh define vm1.xml
    virsh start vm1 --paused
    virsh destroy vm1
    virsh dumpxml vm1 | grep nvram
    sudo virt-fw-vars --inplace <nvram-file> --add-db "$(uuidgen)" keys/db.crt
    virsh start vm1
    virsh console vm1
    ```

    `<nvram-file>` is the path that `virsh dumpxml` prints, such as
    `/var/lib/libvirt/qemu/nvram/vm1_VARS.fd`. virt-fw-vars edits raw edk2 variables files. Where
    a distribution keeps them as qcow2, enrol the certificate in the firmware's setup screen
    instead, as the Proxmox tab describes.

=== "Proxmox"

    Copy the signed `node.raw` to the Proxmox host and create the VM from it. Proxmox imports the
    raw image into its storage, so it needs no qcow2:

    ```sh
    qm create 111 --name vm1 --ostype l26 --machine q35 --bios ovmf --cores 2 --memory 3072 \
      --efidisk0 local-lvm:1,efitype=4m,pre-enrolled-keys=1 \
      --tpmstate0 local-lvm:1,version=v2.0 \
      --virtio0 local-lvm:0,import-from=/root/node.raw \
      --net0 virtio=52:54:00:12:00:11,bridge=vmbr0 \
      --serial0 socket --vga serial0 --agent enabled=1 --boot order=virtio0
    qm disk resize 111 virtio0 32G
    ```

    `111` is the VM ID and `local-lvm` the storage; use yours. `pre-enrolled-keys=1` turns Secure
    Boot on with the distribution's and Microsoft's certificates enrolled. Add your db
    certificate in the firmware's setup screen, from a file on a FAT disk that the VM can read:

    ```sh
    openssl x509 -in keys/db.crt -outform DER -out db.cer
    mkfs.vfat -C db-cert.img 1440
    mcopy -i db-cert.img db.cer ::db.cer
    qm set 111 --virtio1 local-lvm:0,import-from=/root/db-cert.img
    qm start 111
    qm terminal 111
    ```

    Press Esc while the firmware starts. Go to Device Manager, Secure Boot Configuration, and set
    Secure Boot Mode to Custom Mode. Open Custom Secure Boot Options, DB Options, Enroll
    Signature, Enroll Signature Using File, and pick `db.cer` on the small disk. Commit the
    change, leave the setup and reset the VM. Detach and delete the small disk once the node
    runs.

The VM boots its own image, finds no installed node and starts chalkd in maintenance mode. A VM can
also boot the signed installer ISO as a CD-ROM next to an empty virtio disk and install as a
physical machine does, as [Install a node](install.md) describes. That suits a hypervisor that you
cannot copy disk images to.

## Install the node in place

Install the node as a physical machine is installed, from
[Note the address and the fingerprint](install.md#note-the-address-and-the-fingerprint) on. A VM
of the `kvm` platform shows chalkd's mode, fingerprint and addresses on the serial console alone,
and a virtio disk is always `/dev/vda`:

```sh
chalkctl install vm1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

chalkctl checks that the image the VM runs is built for the platform of the node's image, prints
`installing vm1 in place`, and sends no image. Once the node accepted everything, it prints
`vm1 is installed and reboots`. The node gives its ESP a random partition UUID, so that no two VMs
made from the same image share one.

## Check that it worked

[`chalkctl status`](../reference/cli/chalkctl_status.md) shows the node's config, image, platform,
volumes, certificates and failed units:

```sh
chalkctl status vm1 --secrets=secrets.age
```

The hypervisor also sees the guest agent. Under libvirt, `virsh domifaddr` lists the addresses the
agent reports:

```sh
virsh domifaddr vm1 --source agent
```

On Proxmox, the VM's summary shows those addresses.

## If it fails

| Message or symptom | Cause and fix |
| --- | --- |
| The VM's firmware reports `Access Denied` and boots nothing | The image is unsigned or signed with a key whose certificate is not in the VM's db. Sign the image, or enrol `db.crt`. |
| Nothing appears on a VM's console after the firmware | The VM has no serial port, or the console shows the screen. The `kvm` image writes to `ttyS0` only. |
| `the config places the system on /dev/sda (model "...", size ...), but the node runs from /dev/vda (model "...", size ...); install it with the installer instead` | The node's `SystemDisk` names another disk than the one the VM booted from. Fix `spec.disk` in the node's file. |
| `vm1 runs an image built for metal, but its image node-kvm is built for kvm; changing a node's platform is a reinstall` | The VM booted the `metal` image. Make its disk from the signed `images/node-kvm-signed`. |
| A VM's boot after a hypervisor change asks for a recovery key | The VM's UEFI variables or TPM state were replaced. Type the key that `chalkctl recovery-key vm1` prints. |

[If it fails](install.md#if-it-fails) on the install page lists the messages that any install can
show.

## What next

- [Add and remove nodes](add-remove-nodes.md) grows or shrinks the cluster later.
- [Upgrade nodes](upgrade.md) installs a new build of the `kvm` image.

With the Kubernetes layer, continue at [Bootstrap and get a kubeconfig](../kubernetes/bootstrap.md).
