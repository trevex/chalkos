# Runtime access to config and what a delivery does to services; each part is a file beside this
# one. chalkcfg prints the documents, on the system path of every image.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  imports = [
    ./grants.nix
    ./outputs.nix
    ./validators.nix
    ./effects.nix
    ./tests.nix
  ];

  # The installer is no node image and never has config to query.
  environment.systemPackages = lib.mkIf (config.chalkos.image.name != null) [
    pkgs.chalkos.chalkcfg
  ];
}
