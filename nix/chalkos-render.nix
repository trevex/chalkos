# chalkos-render, the renderer of the outputs chalkosLib.render.template and .documents build.
# An image carries it once an output uses it, so it is stripped and takes the standard library
# alone.
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
