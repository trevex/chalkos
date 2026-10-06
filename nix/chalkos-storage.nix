# chalkos-storage, statically linked because it runs in the initrd.
{ lib, buildGoModule }:
buildGoModule {
  pname = "chalkos-storage";
  version = "0.1.0";
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../cmd/chalkos-storage
      ../pkg/storage
    ];
  };
  vendorHash = null;
  subPackages = [ "cmd/chalkos-storage" ];
  env.CGO_ENABLED = "0";
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-storage";
}
