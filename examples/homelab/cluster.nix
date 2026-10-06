{
  imports = [
    ./nodes.nix
    ./extensions/rack.nix
  ];

  chalkos.cluster = {
    name = "homelab";
    # Address held by whichever control-plane node is healthy.
    endpoint = "https://10.0.0.10:6443";
  };

  chalkos.roles.controlplane = { };
  chalkos.roles.worker = { };

  chalkos.rack.enable = true;
}
