# NixOS modules every image is built from.
{
  imports = [
    ./settings.nix
    ./packages.nix
    ./base
    ./runtime
    ./diagnostics.nix
    ./kinds
    ./kernel.nix
    ./chalkd.nix
    ./time.nix
    ./cni.nix
    ./network.nix
    ./layers.nix
    ./kubernetes
    ./router
    ./upgrade.nix
  ];
}
