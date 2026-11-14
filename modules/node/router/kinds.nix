# How a router forwards, on images with the router layer: the families it forwards, its
# reverse-path filter, its multipath hash, its connection table and the interfaces of its node
# API, which the output router-sysctl renders (sysctl.nix) and the firewall reads.
{
  chalkosLib,
  lib,
  routerLib,
}:
let
  inherit (lib) mkOption types;
  inherit (chalkosLib.internal) builtin;
  inherit (chalkosLib.internal.api) groups;
  inherit (routerLib.types)
    interface
    interfaceText
    matchNames
    ;

  # Whether the names s, from matchNames, name the management interface i: they hold i, a name
  # that the glob i's prefix starts, any name for the glob `*`, or a glob whose prefix starts i,
  # which globs, the alternatives of a regular expression, matches.
  names =
    s:
    ''(${s}.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? ${s}.contains(" " + i.substring(0, size(i) - 1)) : ${s}.trim() != "") || ${s}.matches(" (" + globs + ")[*] "))'';
  # The running management interfaces, oldSelf, that a running Network document names and no
  # delivered one does, bound to lost around expr. globs holds the prefixes of i's name, from the
  # empty one to the whole of a name and to the `*` of a glob, with dots escaped.
  bindLost =
    expr:
    ''cel.bind(was, ${matchNames "oldConfig"}, cel.bind(gets, ${matchNames "config"}, cel.bind(lost, oldSelf.filter(i, cel.bind(globs, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15].filter(k, k < size(i) || k == size(i) && !i.endsWith("*")).map(k, i.substring(0, k).replace(".", "[.]")).join("|"), ${names "was"} && !${names "gets"})), ${expr})))'';
in
{
  Router = builtin groups.router // {
    description = "How the router forwards: the families it forwards, its reverse-path filter, how it spreads flows over equal-cost routes, its connection table and the interfaces that reach its node API.";
    spec = {
      forwarding = {
        ipv4 = mkOption {
          type = types.bool;
          default = true;
          description = "Whether the router forwards IPv4 packets between its interfaces.";
        };
        ipv6 = mkOption {
          type = types.bool;
          default = true;
          description = "Whether the router forwards IPv6 packets between its interfaces.";
        };
      };
      rpFilter = mkOption {
        type = types.enum [
          "off"
          "loose"
          "strict"
        ];
        default = "loose";
        description = "Reverse-path filter of the packets the router receives, in both families: `strict` drops a packet whose source the router would not route back out of the interface it came in on, `loose` one whose source it has no route to, and `off` drops none. DHCPv4 between ports 67 and 68 passes.";
      };
      ecmpHash = mkOption {
        type = types.enum [
          "l3"
          "l4"
        ];
        default = "l4";
        description = "What the router hashes to pick one of several equal-cost routes for a flow, in both families: the addresses (`l3`), or the addresses, the protocol and the ports (`l4`).";
      };
      conntrackMax = mkOption {
        type = types.nullOr types.ints.positive;
        default = null;
        description = "Connections the router tracks at most, `net.netfilter.nf_conntrack_max`; null keeps the kernel's default, which follows the memory.";
      };
      management.interfaces = chalkosLib.mkOption {
        type = types.nullOr (types.nonEmptyListOf interface);
        default = null;
        example = [ "eth0" ];
        description = "Interfaces, ${interfaceText}, on which the firewall accepts the node API, chalkd's TCP port 50000; null accepts it on every interface.";
        # A delivery that drops the Network of the interface an operator reaches the router
        # through cuts the operator off, unless it moves the node API too.
        rules = [
          {
            description = "A delivery that keeps the management interfaces keeps a Network naming each management interface that a running Network names, in a [Match] section's Name=; a glob names every interface its prefix starts, so with `*` a delivery keeps one Network at least.";
            rule = "self != oldSelf || oldSelf == null || ${bindLost "size(lost) == 0"}";
            messageExpression = bindLost ''"the config leaves the management interfaces " + lost.join(", ") + " without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery"'';
          }
        ];
      };
    };
  };
}
