# Helpers the built-in kinds share.
let
  api = import ../../../../lib/kinds/api.nix;
in
{
  inherit (api) groups apiVersions;
  # The fields that make a kind of the group a built-in one.
  builtin = group: {
    inherit group;
    inherit (api) version;
    builtin = true;
  };
  # A DNS name: letters, digits, dots and dashes, starting and ending with a letter or digit.
  dnsName = "[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?";
}
