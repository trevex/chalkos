# chalkcfg, which prints a node's config documents. Every image carries it, so it is
# statically linked and stripped, and takes only the packages it imports.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkcfg";
  paths = [
    ../cmd/chalkcfg
    ../pkg/chalkcfg
    ../pkg/config/withheld
    ../pkg/internal/cli
  ];
  subPackages = [ "cmd/chalkcfg" ];
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkcfg";
}
