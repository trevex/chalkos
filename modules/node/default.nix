# NixOS modules every image is built from.
{
  imports = [
    ./settings.nix
    ./base
    ./runtime
    ./diagnostics.nix
    ./kinds
    ./kernel.nix
    ./chalkd.nix
    ./consoles.nix
    ./console.nix
    ./time.nix
    ./network.nix
    ./layers.nix
    ./kubernetes
    ./router
    ./upgrade.nix
  ];
}
