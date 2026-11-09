# The kinds a grant, an output, a validator or an effect names, checked against those the image
# declares: the attributes of chalkos.kinds, given as kinds, and networkd's. kindProblems lists,
# each prefixed with `where`, a kind of names the image does not declare, Secret and a withheld
# kind; checkKinds returns value, or throws them. A unit must not start without the documents it
# expects, so a misspelled kind fails the evaluation.
{
  lib,
  declaredKinds,
  refuse,
}:
rec {
  kindProblems =
    kinds: where: names:
    let
      declared = declaredKinds kinds;
    in
    map (
      k:
      "${where}: the image declares no kind ${k}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declared)}"
    ) (lib.filter (k: !lib.elem k declared) names)
    ++ lib.optional (lib.elem "Secret" names) "${where}: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
    ++ map (
      k:
      "${where}: a unit does not read ${k} documents; the kind is withheld, so they stay in the node's stored config for chalkd alone"
    ) (lib.filter (k: kinds.${k}.withheld or false) names);
  checkKinds =
    kinds: where: names: value:
    refuse (kindProblems kinds where names) value;
}
