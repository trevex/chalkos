# VRFs on images with the router layer: each VRF document declares a kernel VRF, its routing table
# and the links it holds, which the output networkd renders on router images (default.nix). Other
# router kinds name a VRF by its document's name; the default VRF, of every link no VRF holds, has
# no document.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists, so the
# rules bind the members of every VRF as a flat list before they loop over them, and match the
# Network documents' names against one regular expression of the VRFs' members. What depends on
# the links networkd puts in each VRF and on the devices the VRFs create, the validator router
# checks: a management interface or glob that may be a link of a VRF or matches a VRF's device,
# and links two VRFs hold. The output router-networkd checks a NetDev of a VRF's name.
{ chalkosLib, lib }:
let
  inherit (lib) types;
  inherit (chalkosLib.internal) builtin;
  inherit (chalkosLib.internal.api) groups apiVersions;
  inherit (import ../types.nix { inherit chalkosLib lib; }) interface interfaceText matchNames;
  inherit (chalkosLib.internal) cel;

  # The documents of a router kind in the delivered config, by name.
  delivered = kind: cel.documents "config" apiVersions.router kind;
  # The interfaces and globs of every VRF document, as written.
  members = "documents.map(n, documents[n].spec.interfaces).flatten()";
  # The alternatives of a regular expression that matches a name each of held's names and globs
  # covers. Globs match as globs: `*` stands for any characters but a space, and `.` for itself.
  # The interface type admits no other character that RE2 reads as more than itself, so escaping
  # `.` and `*` is enough.
  heldPattern = ''held.map(i, i.replace(".", "[.]").replace("*", "[^ ]*")).join("|")'';
  # Whether the names of matchNames are each held or a negation: a regular expression of
  # heldPattern's alternatives and negations matches the whole string.
  allHeld = ''${matchNames "config"}.matches("^( +(" + ${heldPattern} + "|![^ ]*))* *$")'';
  # Binds, for body, importers to the VRFs whose BGP instances import routes of another VRF and
  # imported to the VRFs they import from, "default" standing for the default VRF. The import
  # families of every instance are bound as one list first: CEL estimates a list flattened in an
  # expression at the product of its lists' sizes, a bound list at one list's.
  leaking =
    body:
    ''cel.bind(bgps, ${delivered "BGP"}, cel.bind(families, bgps.map(n, [bgps[n].spec["import"].ipv4, bgps[n].spec["import"].ipv6]).flatten(), cel.bind(importers, bgps.filter(n, bgps[n].spec["import"].ipv4 != null || bgps[n].spec["import"].ipv6 != null).map(n, bgps[n].spec.vrf), cel.bind(imported, families.filter(f, f != null).map(f, f.vrfs).flatten(), ${body}))))'';
in
{
  VRF = builtin groups.router // {
    multiple = true;
    description = "A VRF of the router: a kernel VRF device of the document's name, its routing table and the links it holds. Other router kinds name it by its name; the default VRF, of every link no VRF holds, has no document.";
    rules = [
      {
        description = "A VRF is named as its device, in 15 characters at most, and not default.";
        rule = ''documents.all(n, n != "default" && size(n) <= 15)'';
        messageExpression = ''documents.filter(n, n == "default" || size(n) > 15).map(n, "VRF " + n).join(", ") + ": a VRF is named as its device, in 15 characters at most, and default names the VRF of every link no VRF holds"'';
      }
      {
        description = "A VRF's name does not start with n64-, which starts the names of the NAT64s' links.";
        rule = ''documents.all(n, !n.startsWith("n64-"))'';
        messageExpression = ''documents.filter(n, n.startsWith("n64-")).map(n, "VRF " + n).join(", ") + ": n64- starts the names of the NAT64s' links; give the VRF another name"'';
      }
      {
        description = "A VRF's table is none of the kernel's own: 253, 254 and 255.";
        rule = "documents.all(n, !(documents[n].spec.table in [253, 254, 255]))";
        messageExpression = ''documents.filter(n, documents[n].spec.table in [253, 254, 255]).map(n, "VRF " + n).join(", ") + " uses table 253, 254 or 255, which the kernel keeps for the default VRF; give it another number"'';
      }
      {
        description = "While the node API answers on every interface, a Network document names a link that no VRF holds; globs match as globs.";
        rule = "size(documents) == 0 || cel.bind(routers, ${delivered "Router"}, routers.exists(k, routers[k].spec.management.interfaces != null)) || cel.bind(held, ${members}, !${allHeld})";
        message = "every link the Network documents name is in a VRF, and chalkd answers in the default VRF alone, on every interface while Router.spec.management.interfaces is null; keep the link you reach the router through out of every VRF";
      }
      {
        description = "A VRF with a conntrack zone neither imports routes from another VRF nor gives its routes to one, through BGP.";
        rule = leaking "documents.all(n, documents[n].spec.conntrackZone == null || !(n in importers) && !(n in imported))";
        messageExpression = leaking ''documents.filter(n, documents[n].spec.conntrackZone != null && (n in importers || n in imported)).map(n, "VRF " + n).join(", ") + ": a VRF with a conntrack zone leaks no routes through a BGP import, as its connections would answer in another zone; drop the zone or the import"'';
      }
    ];
    spec = {
      table = chalkosLib.mkOption {
        type = types.ints.between 1 4294967295;
        unique.hint = "give each VRF a table of its own";
        example = 100;
        description = "Routing table of the VRF, which no other VRF uses; not 253, 254 or 255, the kernel's tables of the default VRF. A change creates the VRF anew, which ends the sessions in it.";
      };
      # An interface or glob is in one VRF, once, compared as written; the validator router
      # refuses globs whose links overlap.
      interfaces = chalkosLib.mkOption {
        type = types.listOf interface;
        unique.hint = "give each interface one VRF";
        default = [ ];
        example = [
          "eth2"
          "swp*"
        ];
        description = "Links the VRF holds, ${interfaceText}; a glob stands for the links the node's Network documents name in a `[Match]` section's `Name=`. A link is named by a Network document, which gets `VRF=`, and every link of that Network is in this VRF.";
      };
      conntrackZone = chalkosLib.mkOption {
        type = types.nullOr (types.ints.between 1 65535);
        unique.hint = "give each VRF a zone of its own";
        default = null;
        example = 1;
        description = "Conntrack zone of the connections through the VRF's links, which no other VRF uses; null shares zone 0 with the default VRF and every VRF without one. A zone keeps the state and NAT of the VRF's connections apart from those of other VRFs with the same addresses. A VRF with a zone neither imports routes from another VRF nor gives its routes to one.";
      };
    };
  };
}
