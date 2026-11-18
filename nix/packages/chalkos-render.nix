# chalkos-render renders chalkosLib.render.template and .documents, the built-in outputs networkd,
# router-sysctl, router-nftables and cluster-registry, and runs the validator router. Every image
# carries it, so it is stripped and takes the standard library, golang.org/x/sys, which the
# identity's imports bring, and these packages alone.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-render";
  paths = [
    ../../cmd/chalkos-render
    # The resolver's DNS syntax, which the rules share.
    ../../pkg/dns
    # The node's static addresses, as chalkd reads them from the Network documents, and the
    # packages the identity imports.
    ../../pkg/identity
    ../../pkg/storage
    ../../pkg/atomicfile
    ../../pkg/internal/strs
    ../../pkg/jsonfile
    # The cluster registry's endpoints, port and firewall table.
    ../../pkg/kubernetes/clusterregistry
    # Writes the networkd kinds' schemas, whose names it reads.
    ../../pkg/networkdschema
    # The format of the refusals chalkd reads.
    ../../pkg/refusals
    # The router's model of a node's config, which chalkctl links for the validator router.
    ../../pkg/router/derive
    # Kea's config types and the per-VRF services' file names, which chalkd reads back.
    ../../pkg/router/keaconf
    ../../pkg/router/services
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
