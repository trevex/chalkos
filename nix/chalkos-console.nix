# chalkos-console, the console on a machine's screen. Every image with chalkos.console.enable
# carries it, so it is statically linked and stripped, and takes only the packages it imports.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-console";
  paths = [
    ../cmd/chalkos-console
    ../pkg/api
    ../pkg/client
    ../pkg/console
    ../pkg/jsonfile
    ../pkg/layers
    ../pkg/pki
    ../pkg/statusview
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
