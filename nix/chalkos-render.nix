# chalkos-render, the renderer of the outputs lib.chalkos.render.template and .documents build, and
# of the built-in outputs networkd, router-sysctl and router-nftables, and the program of the
# validator router. Every image carries it, so it is stripped and takes the standard library alone,
# besides pkg/dns, which shares the resolver's DNS syntax with the rules, pkg/refusals, the format
# of the refusals chalkd reads, and pkg/router/derive, the router's model of a node's config, which
# chalkctl links for the validator router.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-render";
  paths = [
    ../cmd/chalkos-render
    ../pkg/dns
    ../pkg/refusals
    ../pkg/router/derive
  ];
  subPackages = [ "cmd/chalkos-render" ];
  ldflags = [
    "-s"
    "-w"
  ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-render";
}
