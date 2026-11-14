# BGP sessions with bird, the running example of the image guides. The Speaker kind holds the
# node's own side of its sessions and the Peer kind its neighbours. The validator bgp-peers refuses
# an interface the node does not configure, the output bird-conf renders bird's config from them,
# bird reloads when that config changes, and the diagnostic bird-protocols shows bird's sessions.
# bird announces the addresses of the Speaker's interfaces, such as the node's address and the
# Kubernetes API's routed VIP on a dummy interface, and installs the default route its peers send.
# A Peer's password comes from a Secret: bird gets it as a credential, and the rendered config
# names the credential, never the value. A variant that carries this module accepts Speaker and
# Peer documents. The comments # --8<-- mark the sections the guides include.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  bird = config.services.bird.package;
  # An interface's name as bird.conf quotes it: the characters of a Linux interface name, 15 at
  # most, without globs.
  interfaceName = lib.types.strMatching "[A-Za-z0-9_.-]{1,15}";
  # An octet of an IPv4 address in decimal, without leading zeros.
  octet = "25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9]";
  # bird reads no password from a file, and its strings take no escapes: this writes each of the
  # unit's credentials as a constant of bird's config, named as the credential, which bird.conf's
  # password lines name. A name outside letters, digits, '.', '_' and '-', or a value with a quote
  # or a line break, fails the unit. The constants go to a new file that replaces the old one, so
  # bird never reads a file half written or emptied by a failed run.
  credentials = pkgs.writeShellApplication {
    name = "bird-credentials";
    text = ''
      shopt -s nullglob
      umask 077
      tmp=$(mktemp /run/bird/credentials.conf.XXXXXX)
      trap 'rm -f "$tmp"' EXIT
      for file in "''${CREDENTIALS_DIRECTORY-}"/secret_*; do
        name=''${file##*/}
        if [[ ! ''${name#secret_} =~ ^[A-Za-z0-9._-]+$ ]]; then
          echo "$name: bird takes a credential name of letters, digits, '.', '_' and '-' alone" >&2
          exit 1
        fi
        value=$(<"$file")
        if [[ $value == *[$'"\n']* ]]; then
          echo "$name: bird takes no password with a quote or a line break" >&2
          exit 1
        fi
        printf "define '%s' = \"%s\";\n" "$name" "$value"
      done >"$tmp"
      mv "$tmp" /run/bird/credentials.conf
    '';
  };
in
{
  # --8<-- [start:kind]
  chalkos.kinds.Peer = {
    group = "bgp.example.org";
    version = "v1alpha1";
    description = "A BGP neighbour that the node's bird runs a session with on each of the given interfaces.";
    multiple = true;
    spec = {
      # The address lands in bird.conf. chalkctl and chalkd accept only an IPv4 or IPv6 address,
      # which holds nothing that could end bird's statement, and the rule keeps it to IPv6, the
      # family of the sessions bird.conf sets up.
      address = chalkosLib.mkOption {
        type = chalkosLib.types.address;
        example = "fdc8:a1b2:c3d4:1::1";
        description = "IPv6 address of the neighbour, on the subnet of the interfaces the sessions run on.";
        rules = [
          {
            description = "A neighbour has an IPv6 address.";
            rule = ''ip(self).family() == "ipv6"'';
            messageExpression = ''self + " is no IPv6 address; bird runs IPv6 sessions alone"'';
          }
        ];
      };
      # --8<-- [start:rule]
      # A neighbour's AS number is part of who it is: a new one is a new Peer.
      asn = chalkosLib.mkOption {
        type = lib.types.ints.between 1 4294967295;
        example = 65000;
        description = "AS number of the neighbour.";
        rules = [
          {
            description = "A Peer keeps its AS number.";
            rule = "self == oldSelf";
            messageExpression = ''"the AS number stays " + string(oldSelf) + "; add a Peer of another name for " + string(self)'';
          }
        ];
      };
      # --8<-- [end:rule]
      # Each name lands in bird.conf in quotes. A name holds no glob, so a Peer lists each port by its
      # exact name.
      interfaces = chalkosLib.mkOption {
        type = lib.types.nonEmptyListOf interfaceName;
        example = [
          "enp1s0"
          "enp2s0"
        ];
        unique = {
          across = "list";
          hint = "list each interface once";
        };
        description = "Interfaces on which bird runs a session with the neighbour, one each, bound to the interface.";
      };
      password = lib.mkOption {
        type = lib.types.nullOr chalkosLib.types.secretRef;
        default = null;
        example = {
          secretRef = {
            name = "bgp";
            key = "password";
          };
        };
        description = "Key of a Secret that holds the sessions' TCP MD5 password; null for none.";
      };
      bfd = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether BFD watches each session, so that bird ends it within about a second of its link failing.";
      };
    };
  };
  # --8<-- [end:kind]

  # --8<-- [start:speaker]
  # bird takes its router ID from an IPv4 address of the node unless its config gives one, and an
  # IPv6-only node has none, so the Speaker gives it.
  chalkos.kinds.Speaker = {
    group = "bgp.example.org";
    version = "v1alpha1";
    description = "The node's side of its BGP sessions: its AS number, its router ID and the interfaces whose addresses bird announces.";
    spec = {
      asn = lib.mkOption {
        type = lib.types.ints.between 1 4294967295;
        example = 65001;
        description = "AS number of the node.";
      };
      routerID = chalkosLib.mkOption {
        type = lib.types.strMatching "(${octet})([.](${octet})){3}";
        example = "0.0.0.11";
        description = "Router ID of the node in the form of an IPv4 address, unique within the node's AS.";
        rules = [
          {
            description = "A router ID is not 0.0.0.0.";
            rule = ''self != "0.0.0.0"'';
            message = "bird takes no router ID 0.0.0.0; give the node another one, unique within its AS";
          }
        ];
      };
      export.interfaces = lib.mkOption {
        type = lib.types.nonEmptyListOf interfaceName;
        example = [ "node0" ];
        description = "Interfaces whose addresses bird announces to every Peer, such as a dummy interface that holds the node's address.";
      };
    };
  };
  # --8<-- [end:speaker]

  # --8<-- [start:grant]
  # chalkd loads the values the Peers' password fields name as bird's credentials.
  systemd.services.bird.chalkos.kinds = [ "Peer" ];
  # --8<-- [end:grant]

  # --8<-- [start:output]
  # bird.conf, with a session per Peer and interface. bird -p parses a config without running it;
  # the constants the password lines name exist on the node alone, so the check defines each first.
  chalkos.outputs.bird-conf = {
    inputs = [
      "Speaker"
      "Peer"
    ];
    render = chalkosLib.render.template ./bird.conf.tmpl;
    check = ''
      conf=$(mktemp)
      grep -o "'secret_[^']*'" "$OUT/bird.conf" | sort -u | sed 's/.*/define & = "checked";/' >"$conf"
      echo "include \"$OUT/bird.conf\";" >>"$conf"
      ${bird}/bin/bird -p -c "$conf"
    '';
  };
  # --8<-- [end:output]

  # --8<-- [start:validator]
  # bird binds each session to an interface and announces the addresses of the Speaker's
  # interfaces, which the schemas cannot tell the node configures: the validator looks for the
  # names in the node's Network documents, and refuses Peers without a Speaker.
  chalkos.validators.bgp-peers = {
    inputs = [
      "Peer"
      "Speaker"
      "Network"
    ];
    program = chalkosLib.validate.jq ./bgp-peers.jq;
  };
  # --8<-- [end:validator]
  chalkos.tests =
    let
      # A test of bgp-peers: the config of tests/<name>.yaml and the refusals it gets, none
      # unless given.
      validatorTest = name: refusals: {
        "bgp-${name}" = {
          config = ./tests + "/${name}.yaml";
          inherit refusals;
        };
      };
      unconfigured = "has no Network document; bird binds the session to an interface the node configures";
    in
    {
      # The image accepts the example: bgp-peers refuses nothing, bird parses the bird.conf it
      # renders, and that is the golden file's.
      bgp-fabric = {
        config = ./bgp-examples/fabric.yaml;
        outputs.bird-conf = ./tests/fabric/bird-conf;
      };
      # A new password leaves bird.conf as it was, which names the credential alone, and bird
      # reloads to load the credential anew.
      bgp-new-password = {
        before = ./bgp-examples/fabric.yaml;
        config = ./tests/fabric-new-password.yaml;
        effects = [ "bird" ];
      };
    }
    # The refusal docs/build/validate-config.md shows, which the check
    # example-homelab-validator-docs finds in the page.
    // validatorTest "unconfigured-peer-interface" [
      {
        kind = "Peer";
        name = "router";
        path = "spec.interfaces[2]";
        message = "enp3s0 ${unconfigured}";
      }
    ]
    // validatorTest "configured-interfaces" [ ]
    // validatorTest "match-lists" [ ]
    // validatorTest "match-spaced-names" [ ]
    // validatorTest "match-glob" [
      {
        kind = "Peer";
        name = "router";
        path = "spec.interfaces[0]";
        message = "enp1s0 ${unconfigured}";
      }
    ]
    // validatorTest "unconfigured-export" [
      {
        kind = "Speaker";
        name = "bgp";
        path = "spec.export.interfaces[0]";
        message = "node0 has no Network document; bird announces the addresses of an interface the node configures";
      }
    ]
    // validatorTest "peer-without-speaker" [
      {
        kind = "Peer";
        name = "router";
        path = "metadata.name";
        message = "a node with Peer documents needs a Speaker document, which gives bird its AS number and router ID";
      }
    ]
    // validatorTest "no-peers" [ ];

  # --8<-- [start:service]
  services.bird = {
    enable = true;
    # The node's constants, then the rendered config. Neither exists when the image is built, so
    # bird-conf's check parses the config in place of the module's.
    config = ''
      include "/run/bird/credentials.conf";
      include "${config.chalkos.outputs.bird-conf.dir}/bird.conf";
    '';
    checkConfig = false;
  };
  systemd.services.bird.serviceConfig = {
    ExecStartPre = lib.getExe credentials;
    # A reload loads the credentials again and writes the constants anew, so a rotated password
    # reaches bird without a restart.
    RefreshOnReload = "credentials";
    ExecReload = lib.mkForce [
      (lib.getExe credentials)
      "${bird}/bin/birdc configure"
    ];
  };
  # --8<-- [end:service]

  # bird installs the default route its peers send. networkd removes the routes it did not
  # configure itself each time it configures a link, so it leaves them alone here, as on router
  # images. BFD's control packets arrive on UDP port 3784, from a source port of the neighbour's
  # choosing, so no connection the firewall tracks covers them.
  systemd.network.config.networkConfig = {
    ManageForeignRoutes = false;
    ManageForeignNextHops = false;
  };
  networking.firewall.allowedUDPPorts = [ 3784 ];

  # --8<-- [start:effect]
  # bird reads its config again on birdc configure, which keeps the sessions a restart would drop.
  chalkos.effects.bird = chalkosLib.reloadOn { outputs = [ "bird-conf" ]; } [ "bird.service" ];
  # --8<-- [end:effect]

  # --8<-- [start:diagnostic]
  # chalkctl node diag <node> bird-protocols shows bird's sessions. birdc -r asks bird read-only,
  # on bird's socket, which bird makes writable for its group. bird names each session after its
  # Peer and interface, such as router_enp1s0, in quotes, as a name with a hyphen needs them.
  chalkos.diagnostics.bird-protocols = {
    description = "The protocols bird runs and the state of their BGP sessions.";
    command = [
      "${bird}/bin/birdc"
      "-r"
      "show"
      "protocols"
      "'{protocol}'"
    ];
    parameters.protocol = {
      type = "pattern";
      regex = "^[A-Za-z0-9][A-Za-z0-9._-]*$";
      optional = true;
      description = "Name of a protocol, such as router_enp1s0 for the session of the Peer router on enp1s0, which alone is shown; every protocol when left out.";
    };
    role = "reader";
    timeout = "10s";
    groups = [ "bird" ];
  };
  # --8<-- [end:diagnostic]
}
