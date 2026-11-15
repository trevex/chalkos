# The router layer: forwarding, the Router kind and router-sysctl, the router's diagnostics, the
# validator router and routerLib. Each part (firewall, VRFs, BGP, …) is a directory below.
#
# The images allocate the IDs of users and groups without one statically, from 999 down, so an
# unpinned ID would move the IDs after it, and the owners of their files on /var, whenever an
# image adds or drops a feature. The router's parts therefore pin their users' and groups' IDs.
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
  # Whether the image carries every feature of the router.
  allFeatures = lib.all (feature: config.chalkos.router.${feature}.enable) routerLib.features;
  # The tests of the router's examples, router-<name> of each file <name>.yaml.
  exampleTests = chalkosLib.internal.exampleTests "router" config.chalkos.router.examples;
  # The VRF example with the strict reverse-path filter in its Router, in place of the default
  # loose one, the config of the test router-vrf-strict.
  vrfStrict =
    let
      vrf = builtins.readFile ../examples/router/vrf.yaml;
      management = "  management: {interfaces: [eth0]}\n";
      strict = lib.replaceStrings [ management ] [ "${management}  rpFilter: strict\n" ] vrf;
      # The lines of the VRF example that set an rpFilter.
      rpFilters = lib.filter (line: builtins.match "[ ]*rpFilter:.*" line != null) (
        lib.splitString "\n" vrf
      );
    in
    chalkosLib.internal.refuse (
      lib.optional (lib.length (lib.splitString management vrf) != 2) (
        "the VRF example's Router must give its management interfaces once, on the line "
        + "`management: {interfaces: [eth0]}`, after which the test router-vrf-strict sets "
        + "its rpFilter"
      )
      ++ lib.optional (rpFilters != [ ]) (
        "the VRF example sets an rpFilter, which the test router-vrf-strict sets to strict; "
        + "leave the VRF example with the default loose filter"
      )
      ++ lib.optional (exampleTests ? router-vrf-strict) (
        "the router example vrf-strict gives the test router-vrf-strict, which the VRF example "
        + "under the strict filter takes; rename the example"
      )
    ) (pkgs.writeText "vrf-strict.yaml" strict);
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
    description = "The router examples whose documents are all of kinds the image accepts.";
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
      # compare each router output with the golden files of tests/<example>/<output>, or with no
      # file where that directory is missing, as git keeps no empty one. An image that leaves a
      # feature out renders the examples it accepts without comparing them.
      chalkos.tests =
        lib.mapAttrs (
          name: t:
          let
            dir = ./tests + "/${lib.removePrefix "router-" name}";
          in
          t
          // {
            node = "r1";
            outputs = lib.optionalAttrs allFeatures (
              lib.genAttrs routerLib.outputs (
                output:
                let
                  golden = dir + "/${output}";
                in
                if builtins.pathExists golden then golden else pkgs.emptyDirectory
              )
            );
          }
        ) exampleTests
        // lib.optionalAttrs allFeatures {
          # The VRF example under the strict reverse-path filter, whose ruleset accepts the
          # neighbour discovery of link-local sources on the VRFs' devices before the fib check.
          router-vrf-strict = {
            node = "r1";
            config = vrfStrict;
            outputs.router-nftables = ./tests/vrf-strict/router-nftables;
          };
        };

      # zebra installs BGP's routes and next hops, and chalkos-nat64 the routes to the NAT64s'
      # links, which networkd would remove as foreign ones each time it configures a link.
      systemd.network.config.networkConfig = {
        ManageForeignRoutes = false;
        ManageForeignNextHops = false;
      };
    })
  ];
}
