# NixOS modules every role image is built from.
{
  imports = [
    ./settings.nix
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
