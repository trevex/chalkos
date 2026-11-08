# chalkd, the node agent. It takes only the packages it imports, so other Go changes do not
# rebuild the images. withTests keeps the tests in its source, for the image-checker check, which
# builds those of pkg/imagecheck.
{
  callPackage,
  withTests ? false,
}:
callPackage ./go-module.nix { } {
  pname = "chalkd";
  inherit withTests;
  paths = [
    ../cmd/chalkd
    ../pkg/api
    ../pkg/arch
    ../pkg/atomicfile
    ../pkg/chalkd
    ../pkg/check
    ../pkg/client
    ../pkg/clustersecrets
    ../pkg/command
    ../pkg/config
    ../pkg/contract
    ../pkg/diagnostics
    ../pkg/dns
    ../pkg/effects
    ../pkg/esp
    ../pkg/facts
    ../pkg/identity
    ../pkg/image
    ../pkg/imagepart
    ../pkg/imagecheck
    ../pkg/install
    ../pkg/internal/poll
    ../pkg/internal/strs
    ../pkg/jsonfile
    ../pkg/kubernetes
    ../pkg/layers
    ../pkg/nodeconfig
    ../pkg/ociimage
    ../pkg/outputs
    ../pkg/pki
    ../pkg/refusals
    ../pkg/router
    ../pkg/rules
    ../pkg/networkdschema
    ../pkg/schema
    ../pkg/imageref
    ../pkg/secureboot
    ../pkg/storage
    ../pkg/storeslot
    ../pkg/tool
    ../pkg/uki
    ../pkg/upgrade
    ../pkg/verity
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
