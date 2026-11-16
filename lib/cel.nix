# CEL that rules and effects share. A rule reads config and oldConfig, each a config by apiVersion,
# kind and name, and node.
let
  api = import ./kinds/api.nix;
in
rec {
  # The documents of a kind in the config c by name, none when c holds none of the kind.
  documents =
    c: apiVersion: kind:
    ''(("${apiVersion}" in ${c} && "${kind}" in ${c}["${apiVersion}"]) ? ${c}["${apiVersion}"].${kind} : {})'';
  # The specs of the documents of a kind in c, in the order of their names, so a kind a node holds
  # once compares whatever its document's name.
  specs =
    c: apiVersion: kind:
    "cel.bind(d, ${documents c apiVersion kind}, d.map(n, d[n].spec))";
  # Whether the specs of a kind differ between oldConfig and config.
  specsChanged =
    apiVersion: kind: "${specs "config" apiVersion kind} != ${specs "oldConfig" apiVersion kind}";
  # x as a list: a section or a value of networkd's documents is one, or a list of them.
  oneOrMany = x: "(type(${x}) == list ? ${x} : [${x}])";
  # The names and globs of Name= in the [Match] sections of the Network documents of the config c,
  # each value, or each of a list, as one string between spaces: systemd splits a value at spaces
  # and tabs. A list of names would cost a loop more than a string.
  matchNames =
    c:
    let
      networks = documents c api.apiVersions.networkd "Network";
    in
    ''cel.bind(nets, ${networks}, cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? ${oneOrMany "nets[n].spec.match"} : []).flatten()), " " + dyn(matches.map(m, "name" in m ? ${oneOrMany "m.name"} : []).flatten()).join(" ").replace("\t", " ") + " "))'';
}
