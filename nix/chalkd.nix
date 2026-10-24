# chalkd, the node agent. It takes only the packages it imports, so other Go changes do not
# rebuild the images.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkd";
  paths = [
    ../cmd/chalkd
    ../pkg/api
    ../pkg/atomicfile
    ../pkg/chalkd
    ../pkg/client
    ../pkg/command
    ../pkg/config
    ../pkg/effects
    ../pkg/esp
    ../pkg/facts
    ../pkg/identity
    ../pkg/image
    ../pkg/install
    ../pkg/jsonfile
    ../pkg/kubernetes
    ../pkg/networkdschema
    ../pkg/nodeconfig
    ../pkg/ociimage
    ../pkg/outputs
    ../pkg/pki
    ../pkg/rules
    ../pkg/schema
    ../pkg/storage
    ../pkg/storeslot
    ../pkg/tool
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
