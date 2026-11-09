# The dhcp example's server of the default VRF, as kea-dhcp4.service runs it, behind the
# example's firewall, which opens no port for it: Kea's raw sockets read and answer DHCP ahead
# of nftables. A client in a peer namespace, busybox's udhcpc, gets a lease of the pool with the
# subnet's gateway, DNS server and domain, the printer's reserved address and host name by its
# MAC address, and the boot files of BIOS, UEFI and iPXE clients with the boot server. The
# diagnostic dhcp-leases lists the leases, by subnet and by MAC address, the lease file holds
# them, and a reload keeps them.
{
  pkgs,
  lib,
  checkLib,
  unitCheck,
  ...
}:
let
  router = checkLib.router.config;
  inherit (router.systemd.services.kea-dhcp4.serviceConfig) ExecStart;
  firewall = router.systemd.services.chalkos-firewall.serviceConfig.ExecStart;
  dir = router.chalkos.outputs.kea.dir;
  nftDir = router.chalkos.outputs.router-nftables.dir;
  leases = lib.elemAt router.chalkos.diagnostics.dhcp-leases.command 2;
  udhcpc = "${pkgs.busybox}/bin/busybox udhcpc";
  goldens = ../../../cmd/chalkos-render/testdata/router;
in
unitCheck "router-dhcp" checkLib.networkTools ''
  debug() {
    cat kea.log >&2
  }
  ip link set lo up
  netns client
  ip link add lan0 type veth peer name eth0 netns "''${pids[client]}"
  ip addr add 192.0.2.1/24 dev lan0
  ip link set lan0 up
  on_client ip link set lo up
  on_client ip link set eth0 up

  mkdir -p ${dir} ${nftDir}
  install -m 644 ${goldens}/dhcp.nft ${nftDir}/ruleset.nft
  ${firewall} || fail "chalkos-firewall did not load the dhcp example's ruleset"
  awk '/^# kea-dhcp4.json$/ { take = 1; next } /^# / { take = 0 } take' ${goldens}/dhcp.kea >${dir}/kea-dhcp4.json
  grep -q '"office"' ${dir}/kea-dhcp4.json || fail "the dhcp example's golden holds no server of the default VRF"
  mkdir -p /var/lib
  mkdir -m 0750 /run/kea /var/lib/kea
  ${ExecStart} >kea.log 2>&1 &
  kea=$!
  retry 100 "Kea opened no control socket" test -S /run/kea/dhcp4.sock

  # lease <MAC address> [<udhcpc options>]: what the client of the MAC address got: its
  # address, gateway, DNS server, domain, search domain, host name, boot server and boot file.
  cat >bound <<'SCRIPT'
  #!${pkgs.runtimeShell}
  [ "$1" = bound ] || exit 0
  echo "ip=$ip router=$router dns=$dns domain=$domain search=$search hostname=$hostname siaddr=$siaddr boot_file=$boot_file"
  SCRIPT
  chmod +x bound
  lease() {
    on_client ip link set eth0 address "$1"
    shift
    on_client ${udhcpc} -i eth0 -n -q -f -s "$PWD/bound" -t 5 -T 1 -O search "$@" 2>/dev/null
  }
  got() {
    [ "$1" = "$2" ] || fail "$3: the client got '$1', not '$2'"
  }
  pool='ip=192.0.2.100 router=192.0.2.1 dns=192.0.2.1 domain=lan.example.org search=lan.example.org'
  got "$(lease 52:54:00:aa:bb:cc)" "$pool hostname= siaddr= boot_file=" "a client of the pool"
  got "$(lease 52:54:00:12:34:56)" 'ip=192.0.2.10 router=192.0.2.1 dns=192.0.2.1 domain=lan.example.org search=lan.example.org hostname=printer siaddr= boot_file=' "the printer"
  got "$(lease 52:54:00:aa:bb:cc -x 0x5d:0000)" "$pool hostname= siaddr=192.0.2.5 boot_file=undionly.kpxe" "a BIOS client"
  got "$(lease 52:54:00:aa:bb:cc -x 0x5d:0007)" "$pool hostname= siaddr=192.0.2.5 boot_file=ipxe.efi" "a UEFI client of architecture 7"
  got "$(lease 52:54:00:aa:bb:cc -x 0x5d:0009)" "$pool hostname= siaddr=192.0.2.5 boot_file=ipxe.efi" "a UEFI client of architecture 9"
  got "$(lease 52:54:00:aa:bb:cc -x 0x5d:0007 -x 0x4d:69505845)" "$pool hostname= siaddr=192.0.2.5 boot_file=http://192.0.2.5/boot.ipxe" "iPXE on UEFI"
  got "$(lease 52:54:00:aa:bb:cc -x 0x5d:0000 -x 0x4d:69505845)" "$pool hostname= siaddr=192.0.2.5 boot_file=http://192.0.2.5/boot.ipxe" "iPXE on BIOS"

  # listed <arguments>: the leases dhcp-leases lists, without the header and their ends.
  listed() { bash -c ${lib.escapeShellArg leases} dhcp-leases "$@" | tail -n +2 | cut -f 1-5; }
  printer=$(printf 'default\toffice\t192.0.2.10\t52:54:00:12:34:56\tprinter')
  other=$(printf 'default\toffice\t192.0.2.100\t52:54:00:aa:bb:cc\t-')
  got "$(listed)" "$printer"$'\n'"$other" "dhcp-leases"
  got "$(listed --subnet=office --mac=52:54:00:12:34:56)" "$printer" "dhcp-leases of the printer"
  got "$(listed --subnet=guests)" "" "dhcp-leases of another subnet"
  grep -q '^192.0.2.10,52:54:00:12:34:56,' /var/lib/kea/dhcp4.leases || fail "the lease file holds no lease of the printer"
  # A server whose socket closes without an answer is named, and the others still listed.
  ${pkgs.socat}/bin/socat UNIX-LISTEN:/run/kea/dhcp4-ghost.sock,fork SYSTEM:true &
  ghost=$!
  retry 600 "Kea opened no control socket dhcp4-ghost.sock" test -S /run/kea/dhcp4-ghost.sock
  bash -c ${lib.escapeShellArg leases} dhcp-leases >all.tsv 2>errors.txt ||
    fail "dhcp-leases failed beside a server that does not answer: $(cat errors.txt)"
  grep -qx 'the DHCPv4 server of VRF ghost does not answer' errors.txt ||
    fail "dhcp-leases did not name the server that does not answer: $(cat errors.txt)"
  got "$(tail -n +2 all.tsv | cut -f 1-5)" "$printer"$'\n'"$other" "dhcp-leases beside a server that does not answer"
  kill "$ghost"
  rm -f /run/kea/dhcp4-ghost.sock
  kill -HUP "$kea"
  # Kea opens its control socket anew while it reads the config again.
  reloaded() { [ "$(listed 2>/dev/null)" = "$printer"$'\n'"$other" ]; }
  retry 100 "" reloaded || got "$(listed)" "$printer"$'\n'"$other" "dhcp-leases after a reload"
  got "$(lease 52:54:00:12:34:56)" 'ip=192.0.2.10 router=192.0.2.1 dns=192.0.2.1 domain=lan.example.org search=lan.example.org hostname=printer siaddr= boot_file=' "the printer after a reload"
  kill "$kea"
''
