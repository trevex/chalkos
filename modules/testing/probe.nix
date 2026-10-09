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
      jq
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

      # Volumes other than VAR are only wanted by the boot, so they may still be opening.
      wait_for() {
        for _ in $(seq 120); do
          if "$@"; then return 0; fi
          sleep 0.5
        done
        return 1
      }

      # The number of LUKS keyslots: the TPM2 slot plus the fallback once Install enrolled it.
      keyslots() {
        cryptsetup luksDump "$1" 2>/dev/null | grep -cE '^  [0-9]+: luks2' || true
      }

      storage=/state/storage
      # The partition of a volume: VAR on the boot disk, the others by the PARTUUID repart reported.
      partition() {
        local vol=$1 disk uuid
        if [[ $vol == var ]]; then
          echo /dev/disk/chalk-boot/var
          return
        fi
        disk=$(jq -r --arg v "$vol" '.volumes[$v].disk' "$storage/storage.json")
        uuid=$(jq -r --arg d "$disk" --arg v "$vol" '.disks[$d].partitions[$v] // empty' "$storage/disks.json")
        if [[ -n $uuid ]]; then echo "/dev/disk/by-partuuid/$uuid"; fi
      }

      fact root_fstype "$(findmnt -n -o FSTYPE /)"
      # A whole-item match, so options such as errors=remount-ro do not count as read-only.
      fact etc_ro "$(findmnt -n -o OPTIONS /etc | tr ',' '\n' | grep -cx ro || true)"
      fact usr_verity "$(veritysetup status usr 2>/dev/null | awk '$1 == "status:" {print $2}' || true)"
      # The label of the store's partition names the version of the slot the node booted.
      store=$(veritysetup status usr 2>/dev/null | awk '$1 == "data" && $2 == "device:" {print $3}' || true)
      fact store_label "$(if [[ -n $store ]]; then lsblk -no PARTLABEL "$store"; fi)"
      sb=/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c
      fact secureboot "$(od -An -t u1 -j4 -N1 "$sb" 2>/dev/null | tr -d ' ' || true)"
      fact slot_b_empty "$(lsblk -rno PARTLABEL | grep -c '^_empty$' || true)"

      fact state_fstype "$(findmnt -n -o FSTYPE /state || true)"
      fact state_tpm2 "$(cryptsetup luksDump /dev/disk/chalk-boot/state 2>/dev/null | grep -c systemd-tpm2 || true)"
      fact state_keyslots "$(keyslots /dev/disk/chalk-boot/state)"
      if mountpoint -q /state; then fact state_boots "$(count_boots /state/chalktest/boots)"; fi

      if [[ -f $storage/storage.json ]]; then
        for vol in $(jq -r '.volumes | keys[]' "$storage/storage.json"); do
          key=''${vol//-/_}
          part=$(partition "$vol")
          if [[ -z $part ]]; then
            fact "''${key}_missing" 1
            continue
          fi
          wait_for test -b "$part" || true
          fact "''${key}_tpm2" "$(cryptsetup luksDump "$part" 2>/dev/null | grep -c systemd-tpm2 || true)"
          fact "''${key}_keyslots" "$(keyslots "$part")"
          fact "''${key}_size" "$(lsblk -bdno SIZE "$part" 2>/dev/null || true)"
          fact "''${key}_disk" "$(lsblk -no PKNAME "$part" 2>/dev/null || true)"
          dev=$part
          if cryptsetup isLuks "$part" 2>/dev/null; then dev=/dev/mapper/$vol; fi
          mountpoint=$(jq -r --arg v "$vol" '.volumes[$v].mountPoint // empty' "$storage/storage.json")
          if [[ -n $mountpoint ]]; then
            wait_for mountpoint -q "$mountpoint" || true
            fact "''${key}_fstype" "$(findmnt -n -o FSTYPE "$mountpoint" || true)"
            if mountpoint -q "$mountpoint"; then fact "''${key}_boots" "$(count_boots "$mountpoint/.chalktest-boots")"; fi
          else
            wait_for test -b "$dev" || true
            fact "''${key}_block" "$(if [[ -b $dev ]]; then echo 1; else echo 0; fi)"
            fact "''${key}_mounted" "$(findmnt -n -S "$dev" | wc -l)"
          fi
        done
      fi
      # Tests may hard-reset the VM right after "done", which would drop unflushed counters.
      sync
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
