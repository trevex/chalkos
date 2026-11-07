# checkKinds of the config kinds a grant, an output or an effect names, against those the image
# declares: the attributes of chalkos.kinds, given as kinds, and networkd's. It returns value, or
# throws, prefixed with `where`, if names holds a kind the image does not declare, Secret or a
# withheld kind. A unit must not start without the documents it expects, so a misspelled kind
# fails the evaluation.
{ lib, kinds }:
let
  declared = lib.attrNames kinds ++ (import ./api.nix).networkdKinds;
in
where: names: value:
let
  unknown = lib.filter (k: !lib.elem k declared) names;
  withheld = lib.filter (k: kinds.${k}.withheld or false) names;
in
if unknown != [ ] then
  throw "${where}: the image declares no kind ${lib.head unknown}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declared)}"
else if lib.elem "Secret" names then
  throw "${where}: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
else if withheld != [ ] then
  throw "${where}: a unit does not read ${lib.head withheld} documents; the kind is withheld, so they stay in the node's stored config for chalkd alone"
else
  value
