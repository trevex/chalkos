# What the store leaves out. The UKI on the ESP carries the kernel and the initrd, so the system
# does not link them as well: nothing on a node boots from the store's copies. Nodes have no
# logins, so the system path holds none of NixOS's default packages, only what the image's modules
# and the role put there; each unit names the tools it runs in its own path.
{
  config,
  lib,
  options,
  pkgs,
  ...
}:
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.debug.tools = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Add coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system path,
          and crictl on roles with Kubernetes; systemd, bash, less, nftables and kmod are on it
          already. Nodes have no logins; use them from a privileged pod on the node, for example
          `kubectl debug node/<node> -it --image=busybox -- chroot /host /run/current-system/sw/bin/bash`.
        '';
      };
    };
  };

  config = {
    environment.corePackages = lib.mkForce [ ];
    environment.defaultPackages = lib.mkForce [ ];
    environment.systemPackages = lib.mkIf config.chalkos.debug.tools [
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gnused
      pkgs.findutils
      pkgs.procps
      pkgs.iproute2
      pkgs.util-linux
    ];
    programs.nano.enable = false;
    security.sudo.enable = false;
    fonts.fontconfig.enable = false;
    boot.bcache.enable = false;
    boot.kexec.enable = false;
    i18n.defaultLocale = "C.UTF-8";
    i18n.supportedLocales = [ "C.UTF-8/UTF-8" ];

    # xfs_scrub brings python, ICU and GLib; mkfs.xfs, xfs_growfs, xfs_repair and xfs_admin are
    # what nodes run.
    nixpkgs.overlays = [
      (_: prev: {
        xfsprogs = prev.xfsprogs.overrideAttrs (old: {
          configureFlags = old.configureFlags ++ [
            "--enable-scrub=no"
            "--enable-libicu=no"
          ];
          buildInputs = lib.filter (
            p:
            !lib.elem (lib.getName p) [
              "icu4c"
              "python3"
            ]
          ) old.buildInputs;
        });
      })
    ];

    # xfsprogs without xfs_scrub ships no xfs_scrub_all unit, but NixOS sets that unit's PATH,
    # which would generate a unit holding nothing else.
    systemd.suppressedSystemUnits = [ "xfs_scrub_all.service" ];

    # NixOS links both into the system's store path.
    system.systemBuilderCommands = lib.mkAfter ''
      rm $out/kernel $out/initrd
    '';
    # nixos-init reads the system's bootspec in the initrd, so it stays, without the initrd and
    # with the UKI's path on the ESP as the kernel, which a bootspec must name. Nothing boots from
    # that path: only nixos-init's find-etc reads boot.json, for /etc, and the path goes stale once
    # boot counting renames the UKI.
    boot.bootspec.writer = lib.mkForce ''
      ${options.boot.bootspec.writer.default}
      ${lib.getExe pkgs.buildPackages.jq} --sort-keys --arg kernel /efi${config.image.repart.verityStore.ukiPath} \
        '."org.nixos.bootspec.v1" |= (del(.initrd) | .kernel = $kernel)' $out/boot.json >boot.json
      mv boot.json $out/boot.json
    '';
    # The build fails should anything else bring them back. The paths only name what to look for,
    # so the system's build does not depend on them.
    system.forbiddenDependenciesRegexes =
      map (path: "^${lib.escapeRegex (builtins.unsafeDiscardStringContext "${path}")}$")
        [
          config.boot.kernelPackages.kernel
          config.system.build.initialRamdisk
        ];
  };
}
