# Appliance base: no Nix on the node, no switch-to-configuration, tmpfs root, read-only /etc.
{ lib, modulesPath, ... }:
{
  imports = [
    "${modulesPath}/profiles/image-based-appliance.nix"
    "${modulesPath}/profiles/perlless.nix"
  ];

  system.image.id = lib.mkDefault "chalkos";
  system.image.version = lib.mkDefault "0.1.0";
  system.stateVersion = "26.05";

  # The running system lives in tmpfs; persistent data goes to /state and /var (see storage.nix).
  fileSystems."/" = {
    fsType = "tmpfs";
    options = [ "mode=0755" ];
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

  # Nodes resolve names through DNS alone. LLMNR, on by default, has systemd-resolved answer
  # queries for the node's name on every link on port 5355; multicast DNS stays off as well.
  services.resolved.settings.Resolve = {
    LLMNR = false;
    MulticastDNS = false;
  };

  # A read-only /usr leaves nowhere to create /usr/bin/env.
  system.activationScripts.usrbinenv = lib.mkForce "";
}
