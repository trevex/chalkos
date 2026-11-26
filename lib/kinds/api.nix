# The API groups of chalkos's own kinds, their version and apiVersions, networkd's kinds, the
# patterns of names and the normalisers of uniqueness declarations, from
# pkg/constants/constants.json, which chalkos's Go code embeds too. Rules name kinds by apiVersion
# from here rather than from config.chalkos.kinds.<Kind>, since some kinds, such as Flannel, exist
# only on some images.
let
  constants = builtins.fromJSON (builtins.readFile ../../pkg/constants/constants.json);
in
{
  inherit (constants)
    version
    groups
    networkdKinds
    documentName
    plainName
    normalisers
    ;
  apiVersions = builtins.mapAttrs (_: group: "${group}/${constants.version}") constants.groups;
}
