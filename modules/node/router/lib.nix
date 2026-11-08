# What the router's features share: their enable option and the kinds the image declares, which an
# output of one feature reads of another's.
{ config, lib }:
let
  # The kinds the image accepts: its own and networkd's.
  accepted = lib.attrNames config.chalkos.kinds ++ (import ../../../lib/kinds/api.nix).networkdKinds;
in
{
  inherit accepted;

  # The option chalkos.router.<feature>.enable of a feature of the layer, which images carry
  # unless a variant leaves it out.
  feature =
    description:
    lib.mkOption {
      type = lib.types.bool;
      default = true;
      inherit description;
    };

  # The kinds of the list the image accepts, in the list's order: an output reads the kinds of
  # another feature only on images that carry it.
  declared = lib.filter (k: lib.elem k accepted);
}
