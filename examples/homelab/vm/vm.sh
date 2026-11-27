#!/usr/bin/env bash
# The home lab as libvirt VMs on this workstation, under system libvirt (qemu:///system): the
# router and one to three nodes, with the MACs and disk serials of the example's files.
#
#   create [--nodes N] [--secure-boot]  defines the networks, the router and nodes 1 to N
#   status [VM]                         shows the VMs' states and disks and the router's WAN lease
#   start VM                            powers a VM on
#   shutdown VM                         asks a VM to shut down by ACPI
#   poweroff VM                         powers a VM off at once
#   reboot VM                           asks a VM to reboot by ACPI
#   reset VM                            resets a VM at once
#   console VM                          opens a VM's serial console (Ctrl+] leaves it)
#   screen VM                           opens a VM's graphical console in virt-manager
#   insert VM [RAW]                     plugs a copy of the installer's raw image in as a USB stick
#   eject VM                            unplugs the USB stick and deletes its copy
#   link NODE a|b up|down               plugs or pulls the cable of a node's port fabric-a or fabric-b
#   cleanup [VM]                        deletes a VM with its disks, NVRAM and TPM, or the whole lab
#
# The WAN network chalkos-homelab-wan is NATed to the workstation's uplink, with the static lease
# 192.168.246.2 for the router's wan port. The fabric network chalkos-homelab-fabric is isolated
# and carries IPv6 alone, whose addresses the router gives.
set -euo pipefail
export LC_ALL=C

example_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
prefix=chalkos-homelab
wan=$prefix-wan
fabric=$prefix-fabric
pool=default
router_wan_ip=192.168.246.2
router_wan_mac=02:00:00:00:01:01
router_fabric_mac=02:00:00:00:01:02

usage() {
  cat <<'HELP'
Usage: vm/vm.sh COMMAND [VM] [ARG]

VM is router, node1, node2 or node3.

  create [--nodes N] [--secure-boot]  Define the networks, the router and nodes 1 to N (1 to 3,
                                      default 3); run again with a larger N to add nodes
  status [VM]                         Show the VMs' states and disks and the router's WAN lease
  start VM                            Power on
  shutdown VM                         Request a graceful ACPI shutdown
  poweroff VM                         Power off immediately
  reboot VM                           Request a graceful ACPI reboot
  reset VM                            Force a hardware reset
  console VM                          Open the serial console (leave with Ctrl+])
  screen VM                           Open the graphical console in a detached virt-manager
  insert VM [RAW]                     Attach a copy of the installer's raw image as a USB stick
                                      (default: the example's installer/chalkos-installer.raw)
  eject VM                            Detach the USB stick and delete its copy
  link NODE a|b up|down               Plug or pull the cable of the node's port fabric-a or fabric-b
  cleanup [VM]                        Delete a VM with its disks, NVRAM and TPM, or the whole lab

--secure-boot gives new VMs firmware with Secure Boot in setup mode, for chalkctl secureboot enroll.
HELP
}

fail() {
  printf 'vm.sh: %s\n' "$*" >&2
  exit 1
}

virsh() {
  command virsh --connect qemu:///system "$@"
}

# node_mac N PORT prints the MAC of node N's port a or b, as nodes/nodeN.yaml names it.
node_mac() {
  case "$2" in
    a) printf '02:00:00:00:1%s:01' "$1" ;;
    b) printf '02:00:00:00:1%s:02' "$1" ;;
  esac
}

# check_host names what the host lacks: virsh, UEFI firmware (OVMF) or swtpm for the TPM.
check_host() {
  local missing='' caps
  command -v virsh >/dev/null || fail 'virsh is missing: install libvirt and enable libvirtd.'
  caps=$(virsh domcapabilities --machine q35 --arch x86_64 --virttype kvm) ||
    fail 'system libvirt (qemu:///system) does not answer: enable libvirtd and join its group.'
  grep -q "<value>efi</value>" <<<"$caps" || missing+=', OVMF (UEFI firmware for QEMU)'
  grep -q "<value>emulator</value>" <<<"$caps" || missing+=', swtpm (the TPM emulator)'
  [[ -z $missing ]] || fail "libvirt lacks ${missing#, }."
  printf '%s' "$caps"
}

define_networks() {
  if ! virsh net-info "$wan" >/dev/null 2>&1; then
    virsh net-define /dev/stdin <<XML
<network>
  <name>$wan</name>
  <bridge name='virbr-chlwan'/>
  <forward mode='nat'/>
  <ip address='192.168.246.1' prefix='24'>
    <dhcp>
      <range start='192.168.246.100' end='192.168.246.199'/>
      <host mac='$router_wan_mac' name='$prefix-router' ip='$router_wan_ip'/>
    </dhcp>
  </ip>
</network>
XML
  fi

  if ! virsh net-info "$fabric" >/dev/null 2>&1; then
    virsh net-define /dev/stdin <<XML
<network ipv6='yes'>
  <name>$fabric</name>
  <bridge name='virbr-chlfab'/>
</network>
XML
  fi

  local network
  for network in "$wan" "$fabric"; do
    if ! virsh net-list --name | grep -Fxq "$network"; then
      virsh net-start "$network"
    fi
    virsh net-autostart "$network"
  done
}

nic_xml() {
  cat <<XML
    <interface type='network'>
      <mac address='$2'/>
      <source network='$1'/>
      <model type='virtio'/>
      <address type='pci' domain='0x0000' bus='$3' slot='0x00' function='0x0'/>
    </interface>
XML
}

# define_domain VM MEMORY CPUS SIZE NIC NIC defines a VM unless it exists.
define_domain() {
  local vm=$1 memory=$2 cpus=$3 size=$4 nic_a=$5 nic_b=$6
  local name=$prefix-$vm disk=$prefix-$vm.qcow2 secure=no smm=''

  if virsh dominfo "$name" >/dev/null 2>&1; then
    printf '%s is already defined\n' "$name"
    return
  fi

  if ((secure_boot)); then
    secure=yes
    smm="<smm state='on'/>"
  fi

  if ! virsh vol-info --pool "$pool" "$disk" >/dev/null 2>&1; then
    virsh vol-create-as --pool "$pool" "$disk" "$size" --format qcow2
  fi

  # The NICs sit on PCIe root ports of their own, the disk on SATA with the serial the example's
  # SystemDisk selects. With Secure Boot, the firmware enrols no keys and starts in setup mode.
  virsh define --validate /dev/stdin <<XML
<domain type='kvm'>
  <name>$name</name>
  <memory unit='MiB'>$memory</memory>
  <vcpu>$cpus</vcpu>
  <os firmware='efi'>
    <type arch='x86_64' machine='q35'>hvm</type>
    <firmware>
      <feature enabled='$secure' name='secure-boot'/>
      <feature enabled='no' name='enrolled-keys'/>
    </firmware>
    <bootmenu enable='yes' timeout='3000'/>
  </os>
  <features><acpi/><apic/>$smm</features>
  <cpu mode='host-passthrough'/>
  <devices>
    <controller type='pci' index='0' model='pcie-root'/>
    <controller type='pci' index='1' model='pcie-root-port'>
      <model name='pcie-root-port'/>
      <target chassis='1' port='0x10'/>
      <address type='pci' domain='0x0000' bus='0x00' slot='0x02' function='0x0'/>
    </controller>
    <controller type='pci' index='2' model='pcie-root-port'>
      <model name='pcie-root-port'/>
      <target chassis='2' port='0x11'/>
      <address type='pci' domain='0x0000' bus='0x00' slot='0x03' function='0x0'/>
    </controller>
    <controller type='pci' index='3' model='pcie-root-port'>
      <model name='pcie-root-port'/>
      <target chassis='3' port='0x12'/>
      <address type='pci' domain='0x0000' bus='0x00' slot='0x04' function='0x0'/>
    </controller>
    <controller type='sata' index='0'/>
    <controller type='usb' index='0' model='qemu-xhci'/>
    <disk type='volume' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source pool='$pool' volume='$disk'/>
      <target dev='sda' bus='sata'/>
      <serial>chalkos-$vm</serial>
      <boot order='1'/>
      <address type='drive' controller='0' bus='0' target='0' unit='0'/>
    </disk>
$nic_a
$nic_b
    <tpm model='tpm-crb'>
      <backend type='emulator' version='2.0'/>
    </tpm>
    <serial type='pty'><target type='isa-serial' port='0'/></serial>
    <console type='pty'><target type='serial' port='0'/></console>
    <graphics type='vnc' autoport='yes'><listen type='address' address='127.0.0.1'/></graphics>
    <video><model type='vga'/></video>
  </devices>
</domain>
XML
}

create() {
  if ! virsh pool-info "$pool" >/dev/null 2>&1; then
    virsh pool-define-as "$pool" dir --target /var/lib/libvirt/images
  fi
  if ! virsh pool-list --name | grep -Fxq "$pool"; then
    virsh pool-start "$pool"
  fi

  define_networks
  define_domain router 2048 2 32G \
    "$(nic_xml "$wan" "$router_wan_mac" 0x01)" \
    "$(nic_xml "$fabric" "$router_fabric_mac" 0x03)"
  local n
  for ((n = 1; n <= nodes; n++)); do
    define_domain "node$n" 4096 4 64G \
      "$(nic_xml "$fabric" "$(node_mac "$n" a)" 0x01)" \
      "$(nic_xml "$fabric" "$(node_mac "$n" b)" 0x03)"
  done
}

cleanup_domain() {
  local name=$prefix-$1 volume
  if virsh dominfo "$name" >/dev/null 2>&1; then
    if [[ $(virsh domid "$name") != - ]]; then
      virsh destroy "$name" --remove-logs
    fi
    virsh undefine "$name" --nvram --tpm --managed-save --snapshots-metadata --checkpoints-metadata
  fi

  for volume in "$name.qcow2" "$name-installer.raw"; do
    if virsh vol-info --pool "$pool" "$volume" >/dev/null 2>&1; then
      virsh vol-delete --pool "$pool" "$volume"
    fi
  done
}

cleanup() {
  local vm network
  for vm in node3 node2 node1 router; do
    cleanup_domain "$vm"
  done

  for network in "$fabric" "$wan"; do
    if virsh net-list --name | grep -Fxq "$network"; then
      virsh net-destroy "$network"
    fi
    if virsh net-info "$network" >/dev/null 2>&1; then
      virsh net-undefine "$network"
    fi
  done
}

show_status() {
  local name=$prefix-$1
  if ! virsh dominfo "$name" >/dev/null 2>&1; then
    printf '%s: not defined\n' "$name"
    return
  fi
  printf '%s: %s\n' "$name" "$(virsh domstate "$name")"
  virsh domblklist "$name" --details
}

# usb_attached VM [--inactive] succeeds while the VM has the USB stick sdc.
usb_attached() {
  virsh domblklist "$prefix-$1" --details "${@:2}" | awk '$3 == "sdc" { found = 1 } END { exit !found }'
}

insert() {
  local vm=$1 domain=$prefix-$1 raw bytes volume=$prefix-$1-installer.raw
  raw=${2:-$example_dir/installer/chalkos-installer.raw}
  [[ -f $raw ]] || fail "the installer's raw image is missing: $raw (nix build .#installer -o installer)"
  raw=$(realpath -- "$raw")
  if virsh vol-info --pool "$pool" "$volume" >/dev/null 2>&1; then
    fail "$vm has a USB stick already: run vm/vm.sh eject $vm first."
  fi
  # The installer writes to the stick's EFI partition, so each VM gets a copy of its own and the
  # Nix build output stays as built.
  bytes=$(stat -c '%s' -- "$raw")
  virsh vol-create-as --pool "$pool" "$volume" "$bytes" --format raw
  virsh vol-upload --pool "$pool" "$volume" "$raw"
  virsh attach-device "$domain" /dev/stdin --persistent <<XML
<disk type='volume' device='disk'>
  <driver name='qemu' type='raw'/>
  <source pool='$pool' volume='$volume'/>
  <target dev='sdc' bus='usb'/>
  <boot order='2'/>
</disk>
XML
}

eject() {
  local vm=$1 volume=$prefix-$1-installer.raw
  if usb_attached "$vm" --inactive; then
    virsh detach-disk "$prefix-$vm" sdc --persistent
  fi
  if virsh vol-info --pool "$pool" "$volume" >/dev/null 2>&1; then
    if usb_attached "$vm"; then
      fail "$vm has not released the USB stick yet: run vm/vm.sh eject $vm again."
    fi
    virsh vol-delete --pool "$pool" "$volume"
  fi
}

action=${1:-help}
if (($#)); then shift; fi

case "$action" in
  help | -h | --help)
    usage
    exit
    ;;
  create | status | start | shutdown | poweroff | reboot | reset | console | screen | insert | eject | link | cleanup) ;;
  *) fail "unknown command: $action (see vm/vm.sh help)" ;;
esac

caps=$(check_host)

case "$action" in
  create)
    nodes=3 secure_boot=0
    while (($#)); do
      case "$1" in
        --nodes)
          (($# >= 2)) || fail 'usage: vm/vm.sh create [--nodes N] [--secure-boot]'
          nodes=$2
          shift 2
          ;;
        --secure-boot)
          secure_boot=1
          shift
          ;;
        *) fail 'usage: vm/vm.sh create [--nodes N] [--secure-boot]' ;;
      esac
    done
    [[ $nodes == [123] ]] || fail "--nodes takes 1, 2 or 3, not $nodes."
    if ((secure_boot)); then
      secure=$(sed -n "/<enum name='secureBoot'>/,/<\/enum>/p" <<<"$caps")
      [[ $secure == *"<value>yes</value>"* ]] || fail 'libvirt lacks OVMF with Secure Boot.'
    fi
    create
    exit
    ;;
  status)
    if (($# == 0)); then
      for vm in router node1 node2 node3; do
        show_status "$vm"
      done
      if virsh net-info "$wan" >/dev/null 2>&1; then
        virsh net-dhcp-leases "$wan" --mac "$router_wan_mac"
      fi
      exit
    fi
    ;;
  cleanup)
    if (($# == 0)); then
      cleanup
      exit
    fi
    ;;
  link)
    (($# == 3)) || fail 'usage: vm/vm.sh link NODE a|b up|down'
    [[ $1 == node[123] ]] || fail "choose a node: node1, node2 or node3, not $1."
    [[ $2 == [ab] ]] || fail "choose the cable a (fabric-a) or b (fabric-b), not $2."
    [[ $3 == up || $3 == down ]] || fail "choose the state up or down, not $3."
    virsh domif-setlink "$prefix-$1" "$(node_mac "${1#node}" "$2")" "$3"
    exit
    ;;
esac

vm=${1:-}
[[ $vm == router || $vm == node[123] ]] || fail 'choose a VM: router, node1, node2 or node3.'
domain=$prefix-$vm
shift

case "$action" in
  insert) (($# <= 1)) || fail 'usage: vm/vm.sh insert VM [RAW]' ;;
  *) (($# == 0)) || fail "unexpected argument: $1" ;;
esac

case "$action" in
  status) show_status "$vm" ;;
  cleanup) cleanup_domain "$vm" ;;
  start)
    if [[ $(virsh domid "$domain") == - ]]; then
      virsh start "$domain"
    fi
    ;;
  shutdown | reboot) virsh "$action" "$domain" --mode acpi ;;
  poweroff) virsh destroy "$domain" ;;
  reset) virsh reset "$domain" ;;
  console) virsh console "$domain" ;;
  screen)
    command -v virt-manager >/dev/null || fail 'virt-manager is missing, which shows the graphical console.'
    virsh dominfo "$domain" >/dev/null
    exec setsid --fork virt-manager --no-fork --connect qemu:///system \
      --show-domain-console "$domain" </dev/null >/dev/null 2>&1
    ;;
  insert) insert "$vm" "$@" ;;
  eject) eject "$vm" ;;
esac
