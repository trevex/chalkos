# The kinds every image accepts: the node's host name, secrets, OCI registries with their
# credentials and mirrors, storage and time, which chalkd translates into the node's identity.
# Layers declare their kinds in their own modules.
{
  imports = [
    ./core.nix
    ./oci.nix
    ./storage.nix
    ./time.nix
  ];
}
