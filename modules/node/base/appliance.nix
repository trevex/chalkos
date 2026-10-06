# Appliance base: no Nix on the node, no switch-to-configuration, tmpfs root, read-only /etc.
{ lib, modulesPath, ... }:
{
  imports = [
    "${modulesPath}/profiles/image-based-appliance.nix"
    "${modulesPath}/profiles/perlless.nix"
  ];

  system.image.id = "chalkos";
  system.image.version = lib.mkDefault "0.1.0";
  system.stateVersion = "26.05";

  # The running system lives in tmpfs; persistent data goes to /state and /var (see disk.nix).
  fileSystems."/" = {
    fsType = "tmpfs";
    options = [ "mode=0755" ];
  };

  # /etc is assembled from the image at boot and stays read-only. Users are fixed at build time.
  system.etc.overlay.mutable = false;
  services.userborn.static = true;
  # Nodes have no interactive logins, so no account needs a password or SSH key.
  users.allowNoPasswordLogin = true;

  # A read-only /usr leaves nowhere to create /usr/bin/env.
  system.activationScripts.usrbinenv = lib.mkForce "";
}
