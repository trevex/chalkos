# chalkcfg, which prints a node's config documents. Every image carries it, so it is
# statically linked and stripped, and takes only the packages it imports.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkcfg";
  paths = [
    ../cmd/chalkcfg
    ../pkg/chalkcfg
    ../pkg/internal/cli
    ../pkg/internal/strs
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
