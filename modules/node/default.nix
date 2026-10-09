# NixOS modules every role image is built from.
{
  imports = [
    ./settings.nix
    ./base
    ./runtime
    ./chalkd.nix
    ./time.nix
    ./kubernetes.nix
    ./upgrade.nix
  ];
}
