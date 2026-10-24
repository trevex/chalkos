# chalkos-storage, statically linked because it runs in the initrd.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-storage";
  paths = [
    ../cmd/chalkos-storage
    ../pkg/atomicfile
    ../pkg/storage
    ../pkg/tool
  ];
  subPackages = [ "cmd/chalkos-storage" ];
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-storage";
}
