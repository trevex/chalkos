# Appliance base: no Nix on the node, no switch-to-configuration, no shell, tmpfs root, read-only
# /etc.
{
  config,
  lib,
  modulesPath,
  ...
}:
{
  imports = [
    "${modulesPath}/profiles/image-based-appliance.nix"
    "${modulesPath}/profiles/perlless.nix"
  ];

  system.image.id = lib.mkDefault "chalkos";
  system.stateVersion = "26.05";

  # The running system lives in tmpfs; persistent data goes to /state and /var (see storage.nix).
  # Programs run from the store alone: no writable file system lets a file run as a program, take
  # set-user-ID effect or open as a device. The root holds /tmp, /opt and, until the node is
  # installed, /var. The exec paths a layer declares (storage.nix) are the exceptions.
  # systemd mounts /run, /dev and /dev/shm before it reads fstab, and systemd-remount-fs mounts
  # them again with the options fstab gives: NixOS's, which boot.specialFileSystems holds but the
  # systemd initrd does not read, with noexec added.
  fileSystems = {
    "/" = {
      fsType = "tmpfs";
      options = [
        "mode=0755"
        "nosuid"
        "nodev"
        "noexec"
      ];
    };
    "/run" = {
      device = "tmpfs";
      fsType = "tmpfs";
      options = [
        "nosuid"
        "nodev"
        "noexec"
        "strictatime"
        "mode=755"
        "size=${config.boot.runSize}"
      ];
    };
    "/dev" = {
      device = "devtmpfs";
      fsType = "devtmpfs";
      noCheck = true;
      options = [
        "nosuid"
        "noexec"
        "strictatime"
        "mode=755"
        "size=${config.boot.devSize}"
      ];
    };
    "/dev/shm" = {
      device = "tmpfs";
      fsType = "tmpfs";
      options = [
        "nosuid"
        "nodev"
        "noexec"
        "strictatime"
        "mode=1777"
        "size=${config.boot.devShmSize}"
      ];
    };
  };
  # A process may not open a FIFO or regular file in a sticky world-writable directory, such as
  # /tmp, that another user owns, which closes the races of files planted there.
  boot.kernel.sysctl = {
    "fs.protected_fifos" = 2;
    "fs.protected_regular" = 2;
  };

  # /etc is assembled from the image at boot and stays read-only. Users are fixed at build time.
  system.etc.overlay.mutable = false;
  services.userborn.static = true;
  # Nodes have no interactive logins, so no account needs a password or SSH key.
  users.allowNoPasswordLogin = true;

  # The firewall runs on nftables, as kube-proxy and flannel do. Reloading it replaces its own
  # table only, so theirs and chalkos's stay.
  networking.nftables = {
    enable = true;
    flushRuleset = false;
  };
  # The netfilter modules the firewall's rules use, which every image carries.
  chalkos.kernel.groups.firewall.directories = [
    "net/netfilter"
    "net/ipv4/netfilter"
    "net/ipv6/netfilter"
  ];
  chalkos.kernel.moduleGroups = [ "firewall" ];

  # Nodes resolve names through DNS alone. LLMNR, on by default, has systemd-resolved answer
  # queries for the node's name on every link on port 5355; multicast DNS stays off as well.
  services.resolved.settings.Resolve = {
    LLMNR = false;
    MulticastDNS = false;
  };

  # Nodes carry no shell. nixos-init sets the system up in the initrd in place of NixOS's bash
  # activation, and nothing activates the system again: an upgrade boots another image. These are
  # the options of nixpkgs's bashless profile, set one by one, because the profile also turns the
  # firewall off.
  system.nixos-init.enable = true;
  system.activatable = false;
  # No /bin/sh, no shell in the system path or the initrd, and no /etc/shells.
  environment.shell.enable = false;
  programs.bash.enable = false;
  # A read-only /usr leaves nowhere to create /usr/bin/env.
  environment.usrbinenv = null;
  # Each of these brings a bash script: the setuid wrappers, which no chalkos module declares, the
  # sleep hooks, the lingering of user sessions and LVM's resize helper.
  security.enableWrappers = false;
  powerManagement.enable = false;
  users.manageLingering = false;
  services.lvm.resizeHelper.enable = false;
}
