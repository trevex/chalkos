# WireGuard on router images whose chalkos.router.wireguard.enable is true: the router's WireGuard
# interfaces and their peers, which the output networkd renders into a .netdev and a .network per
# interface (default.nix).
#
# What compares the peers of one interface with each other, and the interface with the links the
# Network documents name, the validator router checks: allowed IPs two peers of an interface
# share, a public key two of its peers have, a Network document that names the interface or that
# networkd may pick for it before its own unit, and a link of a VRF other than the interface's. The
# validator also refuses a default route among a peer's allowed IPs and an endpoint whose host is
# no host name, which the schema's pattern lets through.
{ lib }:
let
  inherit (lib) mkOption types;
  inherit (lib.chalkos.internal) builtin;
  inherit (lib.chalkos.internal.api) groups;
  inherit (lib.chalkos.types) ref secretRef;
  inherit (import ../types.nix { inherit lib; })
    prefix
    vrfRef
    delivered
    ;

  # The rule that a secretRef field names its Secret and key as credentials' names, which
  # systemd-networkd's credentials are, secret_<secret>.<key>.
  credentialName = field: {
    description = "A WireGuard key names its Secret and key as credentials' names.";
    rule = ''self == null || self.secretRef.name.matches("^[A-Za-z0-9._-]+$") && self.secretRef.key.matches("^[A-Za-z0-9._-]+$")'';
    message = "the Secret's name and key of ${field} hold letters, digits, '.', '_' and '-' alone, as the names of systemd-networkd's credentials do";
  };
  # A port of the decimal numbers 1 to 65535.
  port = "([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])";
in
{
  WireGuard = builtin groups.router // {
    multiple = true;
    description = "A WireGuard interface of the router, named as the document: its UDP port, its addresses, its VRF and the Secret key that holds its private key. Its peers are WireGuardPeer documents.";
    rules = [
      {
        description = "A WireGuard is named as its interface, in 15 characters at most.";
        rule = "documents.all(n, size(n) <= 15)";
        messageExpression = ''documents.filter(n, size(n) > 15).map(n, "WireGuard " + n).join(", ") + " has a name of more than 15 characters, which is no interface's name; give it a shorter one"'';
      }
      {
        description = "A WireGuard's name does not start with n64-, which starts the names of the NAT64s' links.";
        rule = ''documents.all(n, !n.startsWith("n64-"))'';
        messageExpression = ''documents.filter(n, n.startsWith("n64-")).map(n, "WireGuard " + n).join(", ") + ": n64- starts the names of the NAT64s' links; give the WireGuard another name"'';
      }
      {
        description = "A WireGuard does not take the name of a VRF, whose device has it.";
        rule = "cel.bind(vrfs, ${delivered "VRF"}, documents.all(n, !(n in vrfs)))";
        messageExpression = ''cel.bind(vrfs, ${delivered "VRF"}, documents.filter(n, n in vrfs).map(n, "WireGuard " + n).join(", ") + " takes the name of a VRF, whose device has it; give the WireGuard another name")'';
      }
    ];
    spec = {
      listenPort = lib.chalkos.mkOption {
        type = types.ints.between 1 65535;
        default = 51820;
        unique.hint = "give each WireGuard a port of its own";
        description = "UDP port the interface receives its peers' packets on, which the firewall opens in the default VRF. Two WireGuards take different ports.";
      };
      addresses = lib.chalkos.mkOption {
        type = types.nonEmptyListOf prefix;
        unique = {
          across = "list";
          hint = "give each address once";
        };
        example = [ "fd00:f0::1/64" ];
        description = "Addresses of the interface with their prefix lengths, as `Address=` of a `Network` gives them; the router routes each prefix to the interface. The peers' `allowedIPs` add routes of their own.";
      };
      mtu = mkOption {
        type = types.nullOr (types.ints.between 1280 9000);
        default = null;
        description = "MTU of the interface in bytes, from 1280 to 9000; null takes WireGuard's 1420.";
      };
      privateKey = lib.chalkos.mkOption {
        type = secretRef;
        example = {
          secretRef = {
            name = "wireguard";
            key = "private";
          };
        };
        description = "Key of a Secret that holds the interface's private key, as `wg genkey` prints it. systemd-networkd reads it from its credentials; no rendered file holds it. The Secret's name and key hold letters, digits, `.`, `_` and `-` alone.";
        rules = [ (credentialName "privateKey") ];
      };
      vrf = mkOption {
        type = vrfRef;
        default = "default";
        example = "tenant";
        description = "VRF document that holds the interface; `default` for the default VRF. The routes of the peers' `allowedIPs` go into the VRF's table; the tunnel's own UDP packets travel in the default VRF whatever this says.";
      };
    };
  };

  WireGuardPeer = builtin groups.router // {
    multiple = true;
    description = "A peer of one of the router's WireGuard interfaces: its public key, the address ranges it sends from and the router routes to it, and where the router reaches it.";
    spec = {
      interface = mkOption {
        type = ref "WireGuard";
        example = "wg0";
        description = "WireGuard document of the interface the peer belongs to.";
      };
      publicKey = mkOption {
        type = types.strMatching "[A-Za-z0-9+/]{42}[AEIMQUYcgkosw480]=";
        example = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=";
        description = "Public key of the peer in base64, as `wg pubkey` prints it. The validator `router` refuses a key two peers of one interface have.";
      };
      allowedIPs = lib.chalkos.mkOption {
        type = types.nonEmptyListOf prefix;
        unique = {
          across = "list";
          as = "cidr";
          hint = "name each range once";
        };
        example = [ "fd00:f0::2/128" ];
        description = "Address ranges the router accepts from the peer through the tunnel and sends to it: the router routes each range to the interface in the table of the WireGuard's VRF, a site's networks behind the peer included. The ranges of two peers of one interface do not overlap, and none is a default route, ::/0 or 0.0.0.0/0, which the validator `router` checks.";
      };
      endpoint = mkOption {
        type = types.nullOr (
          types.strMatching "(\\[[0-9A-Fa-f:.]+\\]|[0-9]{1,3}([.][0-9]{1,3}){3}|[A-Za-z0-9]([-A-Za-z0-9.]*[A-Za-z0-9])?):${port}"
        );
        default = null;
        example = "[2001:db8::1]:51820";
        description = "Address and UDP port the router sends the peer's first packets to: `[<IPv6>]:<port>`, `<IPv4>:<port>` or `<host name>:<port>`. null waits for the peer to send first, as a laptop behind NAT does; the router then answers where the peer's packets come from.";
      };
      persistentKeepalive = mkOption {
        type = types.nullOr (types.ints.between 1 65535);
        default = null;
        description = "Seconds between the packets the router sends the peer while the tunnel is idle, which keep a NAT's mapping open; null sends none.";
      };
      presharedKey = lib.chalkos.mkOption {
        type = types.nullOr secretRef;
        default = null;
        description = "Key of a Secret that holds a key the router and the peer share on top of their key pairs, as `wg genpsk` prints it; null for none. systemd-networkd reads it from its credentials. The Secret's name and key hold letters, digits, `.`, `_` and `-` alone.";
        rules = [ (credentialName "presharedKey") ];
      };
    };
  };
}
