# chalkd, the node agent. It takes only the packages it imports, so other Go changes do not
# rebuild role images.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkd";
  paths = [
    ../cmd/chalkd
    ../pkg/api
    ../pkg/chalkd
    ../pkg/client
    ../pkg/identity
    ../pkg/install
    ../pkg/kubernetes
    ../pkg/manifest
    ../pkg/pki
    ../pkg/storage
  ];
  subPackages = [ "cmd/chalkd" ];
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkd";
}
