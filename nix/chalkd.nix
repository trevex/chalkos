# chalkd, the node agent. It takes only the packages it imports, so other Go changes do not
# rebuild the images.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkd";
  paths = [
    ../cmd/chalkd
    ../pkg/api
    ../pkg/chalkd
    ../pkg/client
    ../pkg/config
    ../pkg/identity
    ../pkg/install
    ../pkg/kubernetes
    ../pkg/networkdschema
    ../pkg/nodeconfig
    ../pkg/pki
    ../pkg/schema
    ../pkg/storage
    ../pkg/uki
    ../pkg/upgrade
    ../pkg/verity
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
