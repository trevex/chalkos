# The node's host name and the secrets other documents reference.
{ lib, ... }:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin dnsName;
in
{
  chalkos.kinds = {
    Host = builtin "chalkos.dev" // {
      description = "The host name the node sets at boot.";
      spec.hostname = mkOption {
        type = types.nullOr (types.strMatching dnsName);
        default = null;
        description = "Host name set at boot; null takes the node's name in the inventory.";
      };
    };
    Secret = builtin "chalkos.dev" // {
      multiple = true;
      description = "Secret values that other documents name by reference, such as a Registry's password.";
      spec.data = mkOption {
        type = types.attrsOf types.str;
        description = "Secret values by key, which secretRef fields of other documents name.";
      };
    };
  };
}
