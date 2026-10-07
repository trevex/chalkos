# Core option tree of a chalkos cluster definition.
{
  imports = [
    ./options/cluster.nix
    ./options/secure-boot.nix
    ./options/roles.nix
    ./options/nodes.nix
    ./options/storage.nix
    ./options/kubernetes.nix
    ./options/warnings.nix
    ./options/installer.nix
    ./options/manifest.nix
    ./kubernetes
    ./features/cni
    ./features/dns
  ];
}
