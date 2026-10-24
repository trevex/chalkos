# NixOS modules every image is built from.
{
  imports = [
    ./settings.nix
    ./packages.nix
    ./base
    ./runtime
    ./kinds
    ./kernel.nix
    ./chalkd.nix
    ./time.nix
    ./cni.nix
    ./kubernetes.nix
    ./upgrade.nix
  ];
}
