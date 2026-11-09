# The homelab's extensions: Rack adds the Rack kind and the unit that reads it; Hardware a kind
# rendered into a file a unit reads; BGP bird, its Peer kind and the config rendered from it.
{
  imports = [
    ./rack.nix
    ./hardware.nix
    ./bgp.nix
  ];
}
