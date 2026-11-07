# The kinds every image accepts: the node's host name, secrets, OCI registries with their
# credentials and mirrors, storage and time, which chalkd translates into the node's identity.
# Router images accept the router's kinds as well, which the router layer's outputs render; other
# layers declare their kinds in their own modules.
{
  imports = [
    ./core.nix
    ./oci.nix
    ./storage.nix
    ./time.nix
    ./router.nix
    ./bgp.nix
    ./nat64.nix
    ./vrf.nix
    ./dhcp.nix
    ./dhcp-relay.nix
  ];
}
