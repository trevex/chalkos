# Test-only: prints "CHALKTEST key=value" facts to the console once the system is up,
# so chalklab tests can assert on a node that has no shell access.
{ lib, pkgs, ... }:
let
  probe = pkgs.writeShellApplication {
    name = "chalktest-probe";
    runtimeInputs = with pkgs; [
      coreutils
      util-linux
      cryptsetup
      gnugrep
      gawk
    ];
    text = ''
      fact() { echo "CHALKTEST $1=$2"; }

      count_boots() {
        local file=$1 n=0
        if [[ -f $file ]]; then n=$(cat "$file"); fi
        n=$((n + 1))
        mkdir -p "$(dirname "$file")"
        echo "$n" > "$file"
        echo "$n"
      }

      fact root_fstype "$(findmnt -n -o FSTYPE /)"
      # A whole-item match, so options such as errors=remount-ro do not count as read-only.
      fact etc_ro "$(findmnt -n -o OPTIONS /etc | tr ',' '\n' | grep -cx ro || true)"
      fact usr_verity "$(veritysetup status usr 2>/dev/null | awk '$1 == "status:" {print $2}' || true)"
      sb=/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c
      fact secureboot "$(od -An -t u1 -j4 -N1 "$sb" 2>/dev/null | tr -d ' ' || true)"
      fact slot_b_empty "$(lsblk -rno PARTLABEL | grep -c '^_empty$' || true)"
      for vol in state var; do
        fact "''${vol}_fstype" "$(findmnt -n -o FSTYPE "/$vol" || true)"
        fact "''${vol}_tpm2" "$(cryptsetup luksDump "/dev/disk/by-partlabel/$vol" 2>/dev/null | grep -c systemd-tpm2 || true)"
      done
      if mountpoint -q /state; then fact state_boots "$(count_boots /state/chalktest/boots)"; fi
      if mountpoint -q /var; then fact var_boots "$(count_boots /var/lib/chalktest/boots)"; fi
      fact "done" 1
    '';
  };
in
{
  systemd.services.chalktest-probe = {
    description = "Report chalkos boot facts on the console for chalklab tests";
    wantedBy = [ "multi-user.target" ];
    after = [ "local-fs.target" ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = lib.getExe probe;
      StandardOutput = "journal+console";
    };
  };
}
