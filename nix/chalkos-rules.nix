# chalkos-rules, which checks an image's change rules when the image is built. It runs at build
# time alone, so no image carries it.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-rules";
  paths = [
    ../cmd/chalkos-rules
    ../pkg/atomicfile
    ../pkg/command
    ../pkg/config
    ../pkg/diagnostics
    ../pkg/dns
    ../pkg/effects
    ../pkg/facts
    ../pkg/identity
    ../pkg/jsonfile
    ../pkg/outputs
    ../pkg/refusals
    ../pkg/rules
    ../pkg/schema
    ../pkg/imageref
    ../pkg/storage
    ../pkg/tool
  ];
}
