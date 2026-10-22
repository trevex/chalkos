# chalkos-render, the renderer of the outputs lib.chalkos.render.template and .documents build, and
# of the built-in output networkd. Every image carries it, so it is stripped and takes the standard
# library alone.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-render";
  paths = [ ../cmd/chalkos-render ];
  subPackages = [ "cmd/chalkos-render" ];
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-render";
}
