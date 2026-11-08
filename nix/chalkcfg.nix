# chalkcfg, which prints a node's config documents. Every image carries it, so it is
# statically linked and stripped, and takes only the packages it imports.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkcfg";
  paths = [
    ../cmd/chalkcfg
    ../pkg/atomicfile
    ../pkg/chalkcfg
    ../pkg/config
    ../pkg/identity
    ../pkg/imageref
    ../pkg/internal/cli
    ../pkg/internal/strs
    ../pkg/jsonfile
    ../pkg/networkdschema
    ../pkg/schema
    ../pkg/storage
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
