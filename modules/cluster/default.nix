# Core option tree of a chalkos cluster definition.
{
  imports = [
    ./options/cluster.nix
    ./options/secure-boot.nix
    ./options/roles.nix
    ./options/nodes.nix
    ./options/storage.nix
    ./options/warnings.nix
    ./options/manifest.nix
  ];
}
