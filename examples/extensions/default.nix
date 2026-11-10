# The example's extensions: Rack adds the Rack kind and a unit that reads it; Hardware a kind
# rendered into a file a unit reads.
{
  imports = [
    ./rack.nix
    ./hardware.nix
  ];
}
