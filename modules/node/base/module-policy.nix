# The module policy: the kernel loads modules from the image's verity store and its initrd alone.
# The policy is IPE's, signed with the Secure Boot db key. Its text names the store's root hash,
# which only the built image knows, so the UKI build writes it into a file of SlotSize bytes in
# an uncompressed cpio that the UKI carries before the image's initrd; chalkbuild sign signs it
# there, in place, before it signs the UKI. In the initrd, chalkos-module-policy loads and
# activates it before switch-root and records the outcome, which chalkd reports. A node without
# Secure Boot, or whose db lacks the key, boots without it.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.kernel.modulePolicy;
  exe = lib.getExe pkgs.chalkos.chalkos-module-policy;
  # modulepolicy.SlotSize: the signed policy, a DER PKCS #7 blob with the db certificate, takes
  # about 1.5 KiB with an RSA-2048 key; the rest is NULs.
  slotSize = 16384;
  # chalkos's version as policy_version, whose parts the kernel reads as 16-bit numbers. The
  # kernel keeps no policy across boots, so the version orders policies within one boot alone,
  # and chalkos's versions only increase.
  version =
    let
      m = builtins.match "([0-9]+)\\.([0-9]+)\\.([0-9]+).*" (import ../../../version.nix);
    in
    assert lib.assertMsg (
      m != null && lib.all (p: lib.toInt p <= 65535) m
    ) "version.nix is no version of three parts of at most 65535 each";
    lib.concatStringsSep "." m;
  # The policy, with @ROOT_HASH@ for the store's root hash.
  policy = ''
    policy_name=chalkos policy_version=${version}
    DEFAULT action=ALLOW
    DEFAULT op=KMODULE action=DENY
    op=KMODULE boot_verified=TRUE action=ALLOW
    op=KMODULE dmverity_roothash=sha256:@ROOT_HASH@ action=ALLOW
  '';
  arch =
    {
      x86_64 = "x86-64";
      arm64 = "arm64";
    }
    .${pkgs.stdenv.hostPlatform.linuxArch};
  inherit (config.system.boot.loader) ukiFile;
in
{
  options.chalkos.kernel.modulePolicy.enable = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Whether the kernel loads modules from the image's verity store and its initrd alone. The
      image carries an IPE policy that allows those two and denies every other module, which
      `chalkbuild sign` signs with the Secure Boot db key. A node whose firmware has the key in
      db enforces it from the initrd on; a node without Secure Boot boots without it, and
      `chalkctl status` says so.
    '';
  };

  config = lib.mkIf cfg.enable {
    security.lsm = [ "ipe" ];
    # The kernel's audit records, which journald reads: chalkd finds in them whether enforcement
    # was switched off or another policy activated during the boot.
    boot.kernelParams = [ "audit=1" ];

    system.build.chalkosModulePolicy = policy;

    # The UKI build below passes the initrds itself, the policy's archive first.
    boot.uki.settings.UKI.Initrd = lib.mkForce null;
    # nixpkgs's UKI of the verity store (image/repart-verity-store.nix) with the policy's archive
    # as its first initrd. The archive is uncompressed, so the signed policy replaces the text in
    # place.
    system.build.uki = lib.mkForce (
      pkgs.runCommand ukiFile
        {
          nativeBuildInputs = [
            pkgs.buildPackages.jq
            pkgs.buildPackages.cpio
            pkgs.buildPackages.systemdUkify
          ];
          inherit policy;
          passAsFile = [ "policy" ];
        }
        ''
          mkdir -p $out root/etc/chalkos
          usrhash=$(jq -r '.[] | select(.type=="usr-${arch}-verity") | .roothash' \
            ${config.system.build.intermediateImage}/repart-output.json)
          if [[ ! $usrhash =~ ^[0-9a-f]{64}$ ]]; then
            echo "error: repart reported no root hash of the store: $usrhash" >&2
            exit 1
          fi
          sed "s/@ROOT_HASH@/$usrhash/" $policyPath > root/etc/chalkos/module-policy
          if (( $(stat -c %s root/etc/chalkos/module-policy) > ${toString slotSize} )); then
            echo "error: the module policy is longer than ${toString slotSize} bytes" >&2
            exit 1
          fi
          truncate -s ${toString slotSize} root/etc/chalkos/module-policy
          chmod 0400 root/etc/chalkos/module-policy
          find root -exec touch -h -d @1 {} +
          (cd root && find . -mindepth 1 -printf '%P\n' | sort | cpio -o -H newc -R 0:0 --reproducible --quiet) > policy.cpio
          ukify build \
            --config=${config.boot.uki.configFile} \
            --initrd=policy.cpio \
            --initrd=${config.system.build.initialRamdisk}/${config.system.boot.loader.initrdFile} \
            --cmdline="init=${config.system.build.toplevel}/init ${toString config.boot.kernelParams} usrhash=$usrhash" \
            --output=$out/${ukiFile}
        ''
    );

    boot.initrd.systemd = {
      storePaths = [ exe ];
      # Every module the initrd loads is boot-verified, so the policy only has to be in force
      # before switch-root; it loads before sysinit.target, ahead of udev's coldplug. A failure
      # is recorded for chalkd and does not stop the boot.
      services.chalkos-module-policy = {
        description = "Load the image's kernel module policy";
        wantedBy = [ "sysinit.target" ];
        before = [ "sysinit.target" ];
        unitConfig.DefaultDependencies = false;
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = "${exe} load";
          StandardOutput = "journal+console";
          StandardError = "journal+console";
        };
      };
    };
  };
}
