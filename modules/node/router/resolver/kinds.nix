# DNS on router images whose chalkos.router.resolver.enable is true: a DNS resolver with DNS64 per
# VRF, which the output unbound renders for Unbound (default.nix). A resolver's DNS64 prefix names
# no NAT64 document: it may be that of a NAT64 another router runs.
#
# chalkd refuses a rule that may cost more than two nested loops over a config's lists. So the
# rules over the local zones' records loop over the records flattened. What compares all records
# in pairs, or a resolver with the links of other VRFs, the validator router and the output check:
# a resolver's address of another VRF's links (the validator), and a record twice or a CNAME beside
# another record (output unbound).
{ chalkosLib, lib }:
let
  inherit (lib) mkOption types;
  inherit (chalkosLib.internal) builtin;
  inherit (chalkosLib.internal.api) groups;
  inherit (import ../types.nix { inherit chalkosLib lib; })
    prefix
    address
    vrfRef
    nat64Prefix
    ;

  # Whether the address a is one a service listens on or sends to: no unspecified, link-local,
  # loopback, multicast or IPv4-mapped address.
  unicast =
    a:
    ''cel.bind(x, ip(${a}), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x))'';
  # The declaration that the addresses, or with cidr the ranges, of one list appear once each, in
  # whatever spelling.
  once = as: hint: {
    across = "list";
    inherit as hint;
  };
  # A DNS name in lower case: labels of letters, digits, dashes and underscores, without the root's
  # dot.
  dnsName = types.strMatching "([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?[.])*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?";

  # The records of the local zones, self, flattened. A loop over them costs as much as two nested
  # loops, so their rules call isDNSName() and isRecordData(), which cost what a scan of their
  # string costs, and no regular expression.
  records = "self.map(z, z.records).flatten()";
  # What the data of a record of each type is, as isRecordData() reads it, for a message that says
  # "which is no <data>".
  recordData = ''{"A": "IPv4 address", "AAAA": "IPv6 address", "CNAME": "DNS name in lower case without the root's dot", "PTR": "DNS name in lower case without the root's dot", "MX": "preference from 0 to 65535 and DNS name, a single space apart", "SRV": "priority, weight and port from 0 to 65535 and DNS name, a single space apart", "TXT": "text of 1 to 255 printable characters without quotes or backslashes"}'';
  # Whether a local zone named z holds ipv4only.arpa or a name below it, which DNS64 answers.
  overIPv4Only =
    z:
    ''(${z} == "ipv4only.arpa" || ${z}.endsWith(".ipv4only.arpa") || "ipv4only.arpa".endsWith("." + ${z}))'';

  localZone = types.submodule {
    options = {
      name = mkOption {
        type = dnsName;
        example = "lan.example.org";
        description = "Name of the zone, in lower case and without the root's dot.";
      };
      type = mkOption {
        type = types.enum [
          "static"
          "transparent"
          "typetransparent"
          "redirect"
          "refuse"
          "deny"
          "always_nxdomain"
        ];
        default = "static";
        description = "How Unbound answers names of the zone, as its `local-zone` types say: `static` answers from the records alone, `transparent` resolves the names the records lack, `typetransparent` also the types they lack, `redirect` answers every name below the zone with the zone's own records, `refuse` and `deny` refuse or drop the queries the records do not answer, and `always_nxdomain` answers that no name exists.";
      };
      records = mkOption {
        type = types.listOf (
          types.submodule {
            options = {
              name = mkOption {
                type = dnsName;
                example = "r1.lan.example.org";
                description = "Name of the record: the zone's name or a name below it.";
              };
              type = mkOption {
                type = types.enum [
                  "A"
                  "AAAA"
                  "CNAME"
                  "PTR"
                  "MX"
                  "SRV"
                  "TXT"
                ];
                description = "Type of the record.";
              };
              value = mkOption {
                type = types.strMatching "(]|[ !#-&(-[^-~]){1,255}";
                example = "2001:db8:1::1";
                description = "Data of the record, of up to 255 printable ASCII characters other than quotes and backslashes: an address for `A` and `AAAA`, a name for `CNAME` and `PTR`, `<preference> <name>` for `MX`, `<priority> <weight> <port> <name>` for `SRV`, and text for `TXT`.";
              };
              ttl = mkOption {
                type = types.ints.between 0 2147483647;
                default = 3600;
                description = "Seconds a resolver may keep the record.";
              };
            };
          }
        );
        default = [ ];
        description = "Records the router answers for names of the zone.";
      };
    };
  };
in
{
  Resolver = builtin groups.router // {
    multiple = true;
    description = "A DNS resolver of the router in one VRF, Unbound: the addresses it answers on, whom it answers, where it sends queries, and its DNS64 and local zones.";
    rules = [
      {
        description = "A Resolver listens on an address of a family its clients' ranges are of.";
        rule = ''documents.all(n, cel.bind(s, documents[n].spec, ["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f))))'';
        messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, !["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f)))).map(n, "Resolver " + n).join(", ") + " listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common"'';
      }
      {
        description = "With DNS64, no local zone holds ipv4only.arpa, which Unbound would answer before DNS64.";
        rule = "documents.all(n, cel.bind(s, documents[n].spec, s.dns64 == null || !s.localZones.exists(z, ${overIPv4Only "z.name"})))";
        messageExpression = ''documents.filter(n, cel.bind(s, documents[n].spec, s.dns64 != null && s.localZones.exists(z, ${overIPv4Only "z.name"}))).map(n, "Resolver " + n).join(", ") + " has a local zone over ipv4only.arpa, which Unbound would answer before DNS64; drop the zone or DNS64"'';
      }
    ];
    spec = {
      vrf = chalkosLib.mkOption {
        type = vrfRef;
        unique.hint = "a VRF has one resolver";
        default = "default";
        example = "tenant";
        description = "VRF document on whose links the resolver answers and through whose links it sends its queries; `default` for the default VRF.";
      };
      listen = chalkosLib.mkOption {
        type = types.nonEmptyListOf address;
        example = [ "2001:db8:ffff::1" ];
        description = "Addresses of the router in the resolver's VRF on which it answers on port 53, over UDP and TCP; the firewall accepts DNS on them from `allow`.";
        rules = [
          {
            description = "The resolver listens on unicast addresses.";
            rule = "self.all(a, ${unicast "a"})";
            messageExpression = ''"the address " + self.filter(a, !${unicast "a"})[0] + " is no unicast address the resolver can listen on"'';
          }
        ];
        unique = once "ip" "name each address once";
      };
      allow = chalkosLib.mkOption {
        type = types.nonEmptyListOf prefix;
        example = [ "2001:db8::/32" ];
        description = "Address ranges of the clients the resolver answers; it refuses others, and the firewall accepts DNS from these alone. A family's whole range, `0.0.0.0/0` or `::/0`, answers whoever reaches the addresses of `listen`: an open resolver, if one of them faces the internet.";
        unique = once "cidr" "name each range once";
      };
      forwarders = chalkosLib.mkOption {
        type = types.listOf address;
        default = [ ];
        example = [ "2001:db8::53" ];
        description = "Resolvers the router sends its queries to; empty resolves names itself, from the root servers.";
        rules = [
          {
            description = "The resolver forwards to unicast addresses.";
            rule = "self.all(a, ${unicast "a"})";
            messageExpression = ''"the forwarder " + self.filter(a, !${unicast "a"})[0] + " is no unicast address"'';
          }
        ];
        unique = once "ip" "name each forwarder once";
      };
      dnssec = mkOption {
        type = types.bool;
        default = true;
        description = "Whether the resolver validates DNSSEC with the root's trust anchor of the image. The forwarders then pass DNSSEC records on.";
      };
      dns64 = chalkosLib.mkOption {
        type = types.nullOr (
          types.submodule {
            options.prefix = mkOption {
              type = prefix;
              default = "64:ff9b::/96";
              description = "NAT64 prefix the resolver synthesises AAAA records in, from the A records of names that have no AAAA record.";
            };
            options.synthesizeAll = mkOption {
              type = types.bool;
              default = false;
              description = "Whether the resolver synthesises AAAA records from the A records of every name, and answers with them in place of the name's own AAAA records. A network that reaches the internet over NAT64 alone, such as one with only unique local addresses, has no route to the addresses of those records.";
            };
          }
        );
        default = null;
        example = {
          prefix = "64:ff9b::/96";
        };
        description = "DNS64: AAAA records the resolver synthesises for IPv4-only names, and its answer for `ipv4only.arpa`; null synthesises none.";
        rules = [
          (
            let
              r = nat64Prefix "self.prefix";
            in
            r // { rule = "self == null || (${r.rule})"; }
          )
        ];
      };
      localZones = chalkosLib.mkOption {
        type = types.listOf localZone;
        default = [ ];
        description = "Zones the resolver answers from its own records. A record appears once, and a CNAME record's name holds no other record, which the output `unbound` checks. With `dns64`, no zone holds `ipv4only.arpa`, which DNS64 answers.";
        unique = {
          by = [ "name" ];
          hint = "name each zone once";
        };
        rules = [
          {
            description = "A local zone's records are of the zone's name or names below it.";
            rule = ''self.all(z, z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))'';
            messageExpression = ''cel.bind(z, self.filter(z, !z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))[0], "the local zone " + z.name + " holds a record of " + z.records.filter(r, !(r.name == z.name || r.name.endsWith("." + z.name)))[0].name + ", which is outside it")'';
          }
          {
            description = "The names of local zones and their records are DNS names of 253 characters at most.";
            rule = "self.all(z, isDNSName(z.name)) && ${records}.all(r, isDNSName(r.name))";
            # The schema's pattern takes the labels, so a name this rule refuses is too long.
            messageExpression = ''"the name " + (self.map(z, z.name) + ${records}.map(r, r.name)).filter(n, !isDNSName(n))[0] + " holds more than 253 characters"'';
          }
          {
            description = "A record holds data of its type: an address of its family for A and AAAA, a DNS name for CNAME and PTR, numbers and a DNS name for MX and SRV, and text for TXT.";
            rule = "${records}.all(r, isRecordData(r.type, r.value))";
            messageExpression = ''cel.bind(r, ${records}.filter(r, !isRecordData(r.type, r.value))[0], "the " + r.type + " record of " + r.name + " holds " + r.value + ", which is no " + ${recordData}[r.type])'';
          }
          {
            description = "A redirect zone's records are of the zone's name, which Unbound answers for every name below it.";
            rule = ''self.all(z, z.type != "redirect" || z.records.all(r, r.name == z.name))'';
            messageExpression = ''cel.bind(z, self.filter(z, z.type == "redirect" && !z.records.all(r, r.name == z.name))[0], "the redirect zone " + z.name + " holds a record of " + z.records.filter(r, r.name != z.name)[0].name + "; a redirect zone's records are of its own name, which Unbound answers for every name below it")'';
          }
        ];
      };
    };
  };
}
