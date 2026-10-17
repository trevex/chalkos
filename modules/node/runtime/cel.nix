# CEL that the restart and reload rules of consumers share. A rule reads config and oldConfig,
# each a config by apiVersion, kind and name, and changedKinds.
{ lib }:
{
  # Whether one of the kinds changed: one of its documents, or a Secret one of them references.
  anyChanged = kinds: lib.concatMapStringsSep " || " (kind: ''"${kind}" in changedKinds'') kinds;
}
