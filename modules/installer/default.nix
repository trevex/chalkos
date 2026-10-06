# The installer: the node modules with chalkd in maintenance mode, which writes a role image to
# another disk and installs the node there. It has no storage section and never creates or
# opens STATE, so it leaves the medium it boots from as it is.
#
# It is built as a raw image and as an ISO. The ISO is a hybrid: an ISO 9660 file system holding
# the raw image's ESP as its El Torito boot image, followed by the store partitions, with a GPT
# describing the ESP and the store, so it boots from a CD as well as written to a USB disk.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  image = config.system.build.image;
  losetup = "${config.boot.initrd.systemd.package.util-linux}/bin/losetup";
  raw = "${image}/${config.image.fileName}";
  # GPT partition types of the image's partitions, by the names repart reports.
  gptTypes = {
    esp = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B";
    usr-x86-64 = "8484680C-9521-48C6-9C11-B0720656F69E";
    usr-x86-64-verity = "77FF5F63-E7B6-4633-ACF4-1565B864C0E6";
    usr-arm64 = "B0E01050-EE5F-4390-949A-9101B17104E9";
    usr-arm64-verity = "6E11A4E7-FBCA-4DED-B9E9-E1A512BB664E";
  };
  iso =
    pkgs.runCommand "${config.image.repart.name}-iso"
      {
        nativeBuildInputs = [
          pkgs.xorriso
          pkgs.jq
          pkgs.util-linux
        ];
      }
      ''
        mkdir -p $out tree/boot
        # One line per partition: type, GPT type, PARTUUID, label, offset, size.
        jq -r --argjson types '${builtins.toJSON gptTypes}' \
          '.[] | [.type, $types[.type], .uuid, .label, .offset, .raw_size] | @tsv' \
          ${image}/repart-output.json > partitions.tsv
        args=()
        n=3
        while IFS=$'\t' read -r type gpt uuid label offset size; do
          # The ESP is a file of the ISO 9660 file system, so firmware finds it as the El Torito
          # boot image; the store partitions follow the file system.
          file=$label
          if [[ $type == esp ]]; then file=tree/boot/esp.img; fi
          dd if=${raw} of=$file iflag=skip_bytes,count_bytes skip=$offset count=$size status=none
          if [[ $type != esp ]]; then
            args+=(-append_partition $n $gpt $file)
            n=$((n + 1))
          fi
        done < partitions.tsv

        iso=$out/${config.image.baseName}.iso
        xorriso -as mkisofs -o $iso -V CHALKOS_INSTALLER \
          -e boot/esp.img -no-emul-boot --efi-boot-part --efi-boot-image \
          "''${args[@]}" -appended_part_as_gpt \
          tree

        # The initrd finds the store by the PARTUUIDs derived from its verity root hash, so the
        # GPT must carry the image's partition UUIDs and labels.
        sfdisk --json $iso > table.json
        while IFS=$'\t' read -r type gpt uuid label offset size; do
          n=$(jq -r --arg gpt "$gpt" \
            '.partitiontable.partitions[] | select(.type == $gpt) | .node | capture("(?<n>[0-9]+)$").n' table.json)
          sfdisk --part-uuid $iso "$n" "$uuid"
          sfdisk --part-label $iso "$n" "$label"
        done < partitions.tsv

        # The ISO's partitions in the format of repart-output.json, for chalkctl sign.
        sfdisk --json $iso | jq --argjson types '${builtins.toJSON gptTypes}' '
          ($types | to_entries | map({key: .value, value: .key}) | from_entries) as $names
          | [.partitiontable.partitions[] | select($names[.type])
             | {type: $names[.type], label: .name, uuid: (.uuid | ascii_downcase), offset: (.start * 512), raw_size: (.size * 512)}]' \
          > $iso.json
      '';
in
{
  system.image.id = "chalkos-installer";
  image.repart.name = "chalkos-installer";

  chalkos.disk = {
    # One UKI and a store that is never upgraded.
    espSize = lib.mkDefault "256M";
    storeSize = null;
    storeVeritySize = null;
  };

  # Neither slot B nor STATE on the installer's own medium.
  boot.initrd.systemd.repart.enable = lib.mkForce false;
  boot.initrd.systemd.services.chalkos-state.enable = false;
  boot.initrd.systemd.services.chalkos-storage.enable = false;
  systemd.services.chalkos-identity.enable = false;

  systemd.services.chalkd.environment.CHALKD_INSTALLER = "1";

  # Common disk and CD-ROM controllers, so the installer boots and finds target disks on most
  # machines and VMs.
  boot.initrd.availableKernelModules = [
    "ahci"
    "nvme"
    "sr_mod"
    "uas"
    "usb_storage"
    "virtio_blk"
    "virtio_pci"
    "virtio_scsi"
    "xhci_pci"
  ];
  boot.kernelParams = [
    "console=tty0"
    "console=ttyS0,115200"
  ];

  # A CD-ROM has no partitions in Linux; a partitioned loop device over it exposes the store
  # partitions the GPT of the ISO describes.
  boot.initrd.systemd.storePaths = [ losetup ];
  boot.initrd.services.udev.rules = ''
    SUBSYSTEM=="block", KERNEL=="sr[0-9]*", ACTION=="add", RUN+="${losetup} --find --partscan --read-only $devnode"
  '';

  system.build.chalkosInstaller = pkgs.runCommand "chalkos-installer" { } ''
    mkdir $out
    ln -s ${image}/* $out/
    ln -s ${iso}/* $out/
  '';
}
