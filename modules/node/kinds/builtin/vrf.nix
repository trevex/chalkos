# VRFs on images with the router layer: each VRF document declares a kernel VRF, its routing table
# and the links it holds, which the output networkd renders on router images
# (modules/node/router/vrf.nix). Other router kinds name a VRF by its document's name; the default
# VRF, of every link no VRF holds, has no document.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists, so the
# rules bind the members of every VRF, the management interfaces and the NetDev devices as flat
# lists before they loop over them, and match the Network documents' names and the management
# interfaces against one regular expression of the VRFs' members.
{
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin groups apiVersions;
  inherit (import ./router-types.nix { inherit lib; }) interface interfaceText matchNames;
  cel = import ../../../../lib/cel.nix { inherit lib; };

  # The documents of a router kind in the delivered config, by name.
  delivered = kind: cel.documents "config" apiVersions.router kind;
  # The interfaces and globs of every VRF document, as written.
  members = "documents.map(n, documents[n].spec.interfaces).flatten()";
  # The interfaces of Router.spec.management.interfaces, as written.
  managed = "cel.bind(routers, ${delivered "Router"}, routers.map(k, routers[k].spec.management.interfaces == null ? [] : routers[k].spec.management.interfaces).flatten())";
  # The devices the config's NetDev documents create, by the Name= of their [NetDev] sections.
  # has() tests a key, where `in` would cost a loop more.
  devices = ''cel.bind(netdevs, ${
    cel.documents "config" apiVersions.networkd "NetDev"
  }, netdevs.map(n, !has(netdevs[n].spec.netDev) ? [] : ${cel.oneOrMany "netdevs[n].spec.netDev"}.map(s, has(s.name) ? s.name : "")).flatten())'';
  # The alternatives of a regular expression that matches a name each of held's names and globs
  # covers. Globs match as globs: `*` stands for any characters but a space, and `.` for itself.
  # The interface type admits no other character that RE2 reads as more than itself, so escaping
  # `.` and `*` is enough.
  heldPattern = ''held.map(i, i.replace(".", "[.]").replace("*", "[^ ]*")).join("|")'';
  # Whether the names of matchNames are each held or a negation: a regular expression of
  # heldPattern's alternatives and negations matches the whole string.
  allHeld = ''${matchNames "config"}.matches("^( +(" + ${heldPattern} + "|![^ ]*))* *$")'';
  # Binds, for body, heldName to a regular expression that matches the names held's members cover,
  # and heldText to held's members, each after a space. A test per pair of a member and another
  # interface would cost more than chalkd allows.
  heldBy =
    body:
    ''cel.bind(heldName, "^(" + ${heldPattern} + ")$", cel.bind(heldText, held.map(i, " " + i).join(""), ${body}))'';
  # Whether the interface or glob i has a name in common with a member of held, inside heldBy: a
  # member covers i, as it covers a glob it is a shorter prefix of, or i is a glob whose prefix
  # starts a member.
  overlapsHeld =
    i:
    ''(${i}.matches(heldName) || ${i}.endsWith("*") && heldText.contains(" " + ${i}.replace("*", "")))'';
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds.VRF = builtin groups.router // {
      multiple = true;
      description = "A VRF of the router: a kernel VRF device of the document's name, its routing table and the links it holds. Other router kinds name it by its name; the default VRF, of every link no VRF holds, has no document.";
      rules = [
        {
          description = "A VRF is named as its device, in 15 characters at most, and not default.";
          rule = ''documents.all(n, n != "default" && size(n) <= 15)'';
          messageExpression = ''documents.filter(n, n == "default" || size(n) > 15).map(n, "VRF " + n).join(", ") + ": a VRF is named as its device, in 15 characters at most, and default names the VRF of every link no VRF holds"'';
        }
        {
          description = "A VRF's table is none of the kernel's own: 253, 254 and 255.";
          rule = "documents.all(n, !(documents[n].spec.table in [253, 254, 255]))";
          messageExpression = ''documents.filter(n, documents[n].spec.table in [253, 254, 255]).map(n, "VRF " + n).join(", ") + " uses table 253, 254 or 255, which the kernel keeps for the default VRF; give it another number"'';
        }
        {
          description = "A table belongs to one VRF.";
          rule = "cel.bind(t, documents.map(n, documents[n].spec.table), t.all(x, size(t.filter(y, y == x)) == 1))";
          messageExpression = ''cel.bind(t, documents.map(n, documents[n].spec.table), "more than one VRF uses table " + string(t.filter(x, size(t.filter(y, y == x)) > 1)[0]) + "; give each VRF a table of its own")'';
        }
        {
          description = "An interface or glob is in one VRF, once; globs compare as written.";
          rule = "cel.bind(all, ${members}, all.all(i, size(all.filter(j, j == i)) == 1))";
          messageExpression = ''cel.bind(all, ${members}, "the interface " + all.filter(i, size(all.filter(j, j == i)) > 1)[0] + " is in more than one VRF, or twice in one; give each interface one VRF")'';
        }
        {
          description = "No VRF holds an interface of Router.spec.management.interfaces; globs match as globs.";
          rule = "cel.bind(held, ${members}, cel.bind(api, ${managed}, ${heldBy "api.all(i, !${overlapsHeld "i"})"}))";
          messageExpression = "cel.bind(held, ${members}, cel.bind(api, ${managed}, ${heldBy ''"the management interface " + api.filter(i, ${overlapsHeld "i"})[0] + " is in a VRF, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"''}))";
        }
        {
          description = "While the node API answers on every interface, a Network document names a link that no VRF holds; globs match as globs.";
          rule = "size(documents) == 0 || cel.bind(routers, ${delivered "Router"}, routers.exists(k, routers[k].spec.management.interfaces != null)) || cel.bind(held, ${members}, !${allHeld})";
          message = "every link the Network documents name is in a VRF, and chalkd answers in the default VRF alone, on every interface while Router.spec.management.interfaces is null; keep the link you reach the router through out of every VRF";
        }
        {
          description = "No NetDev document creates a device of a VRF's name.";
          rule = "cel.bind(made, ${devices}, documents.all(n, !(n in made)))";
          messageExpression = ''cel.bind(made, ${devices}, "the VRF " + documents.filter(n, n in made)[0] + " takes the name of a device a NetDev document creates; rename the VRF or the NetDev")'';
        }
      ];
      spec = {
        table = mkOption {
          type = types.ints.between 1 4294967295;
          example = 100;
          description = "Routing table of the VRF, which no other VRF uses; not 253, 254 or 255, the kernel's tables of the default VRF. A change creates the VRF anew, which ends the sessions in it.";
        };
        interfaces = mkOption {
          type = types.listOf interface;
          default = [ ];
          example = [
            "eth2"
            "swp*"
          ];
          description = "Links the VRF holds, ${interfaceText}; a glob stands for the links the node's Network documents name in a `[Match]` section's `Name=`. A link is named by a Network document, which gets `VRF=`, and every link of that Network is in this VRF.";
        };
      };
    };
  };
}
