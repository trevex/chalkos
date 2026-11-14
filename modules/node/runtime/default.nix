# Runtime access to config and what a delivery does to services: grants of config kinds to units
# (grants.nix), files rendered from documents (outputs.nix), programs that check the documents
# (validators.nix), the restarts, reloads and commands a delivery takes (effects.nix) and the
# image's tests of what it makes of a config (tests.nix).
# chalkcfg prints the documents, on the system path of every image.
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

  # The installer is no variant and never has config to query.
  environment.systemPackages = lib.mkIf (config.chalkos.variant.name != null) [
    pkgs.chalkos.chalkcfg
  ];
}
