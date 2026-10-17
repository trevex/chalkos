# chalkos-rules, which checks an image's change rules when the image is built. It runs at build
# time alone, so no image carries it.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-rules";
  paths = [
    ../cmd/chalkos-rules
    ../pkg/config
    ../pkg/facts
    ../pkg/identity
    ../pkg/kubernetes
    ../pkg/rules
    ../pkg/schema
    ../pkg/storage
  ];
  subPackages = [ "cmd/chalkos-rules" ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-rules";
}
