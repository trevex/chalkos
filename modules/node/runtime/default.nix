# Runtime access to config and what a delivery does to services: grants of config kinds to units
# (grants.nix), files rendered from documents (outputs.nix) and the restarts, reloads and commands
# a delivery takes (effects.nix). chalkcfg prints the documents, on the system path of every
# image.
{
  chalkosPackages,
  config,
  lib,
  ...
}:
{
  imports = [
    ./grants.nix
    ./outputs.nix
    ./effects.nix
  ];

  # The installer is no variant and never has config to query.
  environment.systemPackages = lib.mkIf (config.chalkos.variant.name != null) [
    chalkosPackages.chalkcfg
  ];
}
