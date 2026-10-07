# NixOS modules every role image is built from.
{
  imports = [
    ./settings.nix
    ./base
    ./runtime
    ./chalkd.nix
    ./kubernetes.nix
  ];
}
