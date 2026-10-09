# What the store leaves out. The UKI on the ESP carries the kernel and the initrd, so the system
# does not link them as well: nothing on a node boots from the store's copies. Nodes have no
# logins, so the system path is empty and each unit names the tools it runs in its own path.
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
          Put bash, coreutils, util-linux, iproute2, procps, less, nftables and kmod on the system
          path, which is otherwise empty: `environment.systemPackages` holds these alone. Nodes
          have no logins; use them from a privileged pod on the node, for example
          `kubectl debug node/<node> -it --image=busybox -- chroot /host /run/current-system/sw/bin/bash`.
        '';
      };
    };
  };

  config = {
    environment.systemPackages = lib.mkForce (
      lib.optionals config.chalkos.debug.tools [
        pkgs.bashInteractive
        pkgs.coreutils
        pkgs.util-linux
        pkgs.iproute2
        pkgs.procps
        pkgs.less
        pkgs.nftables
        pkgs.kmod
      ]
    );
    programs.nano.enable = false;
    security.sudo.enable = false;
    fonts.fontconfig.enable = false;
    boot.bcache.enable = false;
    boot.kexec.enable = false;
    i18n.defaultLocale = "C.UTF-8";
    i18n.supportedLocales = [ "C.UTF-8/UTF-8" ];
    # NixOS gives D-Bus systemd's services and policies only through the system path; the
    # kubelet's cgroup driver, logind and networkd reach systemd over D-Bus.
    services.dbus.packages = [ config.systemd.package ];

    # NixOS links both into the system's store path.
    system.systemBuilderCommands = lib.mkAfter ''
      rm $out/kernel $out/initrd
    '';
    # nixos-init reads the system's bootspec in the initrd, so it stays, without the initrd and
    # with the UKI's path on the ESP as the kernel, which a bootspec must name.
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
