{
  imports = [
    ./nodes.nix
    ./extensions/rack.nix
  ];

  chalkos.cluster = {
    name = "homelab";
    # cp1's address: the API server of the only control-plane node.
    endpoint = "https://10.0.0.11:6443";
  };

  # One image for every node; cp1's kind makes it the control plane.
  chalkos.roles.node = { };

  chalkos.rack.enable = true;
}
