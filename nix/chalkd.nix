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
    ../pkg/check
    ../pkg/client
    ../pkg/clustersecrets
    ../pkg/command
    ../pkg/config
    ../pkg/diagnostics
    ../pkg/dns
    ../pkg/effects
    ../pkg/esp
    ../pkg/facts
    ../pkg/identity
    ../pkg/image
    ../pkg/imagecheck
    ../pkg/install
    ../pkg/jsonfile
    ../pkg/kubernetes
    ../pkg/layers
    ../pkg/networkdschema
    ../pkg/nodeconfig
    ../pkg/ociimage
    ../pkg/outputs
    ../pkg/pki
    ../pkg/refusals
    ../pkg/router
    ../pkg/rules
    ../pkg/schema
    ../pkg/imageref
    ../pkg/secureboot
    ../pkg/storage
    ../pkg/storeslot
    ../pkg/tool
    ../pkg/uki
    ../pkg/upgrade
    ../pkg/validators
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
