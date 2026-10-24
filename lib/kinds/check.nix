# checkKinds of the config kinds a grant, an output or an effect names, against those the image
# declares: the attributes of chalkos.kinds, given as kinds, and networkd's. It returns value, or
# throws, prefixed with `where`, if names holds a kind the image does not declare, Secret or
# Addon. A unit must not start without the documents it expects, so a misspelled kind fails the
# evaluation.
{ lib, kinds }:
let
  declared = lib.attrNames kinds ++ (import ./api.nix).networkdKinds;
in
where: names: value:
let
  unknown = lib.filter (k: !lib.elem k declared) names;
in
if unknown != [ ] then
  throw "${where}: the image declares no kind ${lib.head unknown}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declared)}"
else if lib.elem "Secret" names then
  throw "${where}: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
else if lib.elem "Addon" names then
  throw "${where}: a unit does not read Addon documents; they stay in the node's stored config, and chalkd applies their objects on control planes"
else
  value
