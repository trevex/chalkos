# The router layer, on images whose nodes route between networks: its marker, forwarding, the
# Router kind and its output router-sysctl, the router's diagnostics, the validator router and
# routerLib, the module argument of the router's parts.
# Each part of the router has a directory of its own with its kinds, outputs, effects, services
# and diagnostics: the firewall, which takes the place of NixOS's, VRFs and policy routing, and
# the features a variant may leave out, each with its option chalkos.router.<feature>.enable:
# BGP with FRR, NAT64 with Jool, the resolver with Unbound, DHCP with Kea, DHCP relays with
# dnsmasq and WireGuard with systemd-networkd.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  # What the router's parts share, which they get as the module argument routerLib, as NixOS's
  # modules get utils.
  routerLib = import ./lib.nix {
    inherit
      chalkosLib
      config
      lib
      pkgs
      ;
  };
  inherit (routerLib) accepted;
  # Whether the image carries every feature of the router: the attribute sets of chalkos.router
  # are the features, each with its enable option.
  allFeatures = lib.all (feature: feature.enable) (
    lib.filter lib.isAttrs (lib.attrValues config.chalkos.router)
  );
in
{
  imports = [
    ./firewall
    ./sysctl.nix
    ./diagnostics.nix
    ./bgp
    ./nat64
    ./resolver
    ./vrf
    ./policy-routing
    ./dhcp
    ./dhcp-relay
    ./wireguard
    ./validator.nix
  ];

  options.chalkos.router.enable = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether the image carries the router layer, which forwards IPv4 and IPv6 packets between
      the node's interfaces and accepts the kinds of `router.chalkos.dev`. An image carries the
      Kubernetes layer or the router layer, not both. mkImages sets it from the variant's
      `router`; the installer carries no layer.
    '';
  };

  options.chalkos.router.examples = lib.mkOption {
    type = lib.types.path;
    internal = true;
    readOnly = true;
    default =
      let
        # The kinds of an example's documents. Each document names its kind on an unindented
        # kind: line of its own, bare or quoted.
        kindsOf =
          file:
          map (m: lib.elemAt m 1) (
            lib.filter lib.isList (
              builtins.split "(^|\n)kind:[ \t]*[\"']?([A-Za-z0-9]+)" (builtins.readFile file)
            )
          );
      in
      builtins.path {
        name = "router-examples";
        path = ../examples/router;
        # A link would be copied as it is, past the kinds' filter, so the examples are files.
        filter =
          file: type:
          if type == "directory" then
            true
          else if type == "regular" then
            lib.all (k: lib.elem k accepted) (kindsOf file)
          else
            throw "the router example ${baseNameOf file} is a ${type}, not a file";
      };
    description = ''
      The config files the router's tests run, the test `router-<name>` of `chalkos.tests` the
      file `<name>.yaml`: the files of modules/node/examples/router all of whose documents are
      of kinds the image accepts. An image whose variant leaves a router feature out refuses
      that feature's kinds, and runs the tests of the examples without them.
    '';
  };

  config = lib.mkMerge [
    # Every image declares the parts' options, which read routerLib, so it is set beside the
    # condition rather than under it: under it, an image without the router layer fails with
    # routerLib missing as soon as anything reads one of those options.
    { _module.args.routerLib = routerLib; }
    (lib.mkIf config.chalkos.router.enable {
      chalkos.kinds = routerLib.kinds ./kinds.nix;
      chalkos.layers.router = "";
      chalkos.forwarding.enable = true;
      # The examples' tests run as the node r1 and, on an image with every feature of the router,
      # compare the outputs with the golden files of tests/<example>/<output>. An image that leaves
      # a feature out renders the examples it accepts without comparing them.
      chalkos.tests = lib.mapAttrs (
        name: t:
        let
          dir = ./tests + "/${lib.removePrefix "router-" name}";
        in
        t
        // {
          node = "r1";
          outputs = lib.optionalAttrs (allFeatures && builtins.pathExists dir) (
            lib.mapAttrs (output: _: dir + "/${output}") (builtins.readDir dir)
          );
        }
      ) (chalkosLib.internal.exampleTests "router" config.chalkos.router.examples);

      # zebra installs BGP's routes and next hops, and chalkos-nat64 the routes to the NAT64s'
      # links, which networkd would remove as foreign ones each time it configures a link.
      systemd.network.config.networkConfig = {
        ManageForeignRoutes = false;
        ManageForeignNextHops = false;
      };
    })
  ];
}
