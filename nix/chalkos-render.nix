# chalkos-render, the renderer of the outputs chalkosLib.render.template and .documents build, and
# of the built-in outputs networkd, router-sysctl and router-nftables, and the program of the
# validator router. Every image carries it, so it is stripped and takes the standard library alone,
# besides pkg/dns, which shares the resolver's DNS syntax with the rules, pkg/networkdschema, which
# writes the networkd kinds' schemas whose names it reads, pkg/refusals, the format of the refusals
# chalkd reads, pkg/router/derive, the router's model of a node's config, which chalkctl links
# for the validator router, pkg/router/services, the names of the per-VRF services' files, and
# pkg/router/keaconf, the types of Kea's configs, both of which chalkd reads back.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-render";
  paths = [
    ../cmd/chalkos-render
    ../pkg/dns
    ../pkg/networkdschema
    ../pkg/refusals
    ../pkg/router/derive
    ../pkg/router/keaconf
    ../pkg/router/services
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
