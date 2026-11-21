# The diagnostics every image declares: the node's addresses, interfaces, routes, routing policy
# rules and neighbours, the route to an address, ping, DNS queries, TCP connects and the nftables
# ruleset. Those that send traffic from the node or show its firewall need an operator. Each tool
# is the smallest that does the job, as every image carries them: ip alone rather than iproute2,
# ldns's drill rather than dig, and chalkd itself for a TCP connect.
{
  lib,
  pkgs,
  ...
}:
let
  ip = lib.getExe pkgs.chalkos.iproute2-ip;
  address = {
    type = "address";
    description = "IPv4 or IPv6 address.";
  };
  family = {
    type = "enum";
    values = [
      "inet"
      "inet6"
    ];
    default = "inet";
    description = "Address family: inet for IPv4, inet6 for IPv6.";
  };
  # A diagnostic that only reads the node's state.
  reader = {
    role = "reader";
    timeout = "10s";
  };
in
{
  chalkos.diagnostics = {
    addresses = reader // {
      description = "The addresses of the node's interfaces, as ip -json address show prints them.";
      command = [
        ip
        "-json"
        "address"
        "show"
      ];
    };
    links = reader // {
      description = "The node's network interfaces and their state, as ip -json link show prints them.";
      command = [
        ip
        "-json"
        "link"
        "show"
      ];
    };
    # With a table, ip lists one family's routes; inet is its own default.
    routes = reader // {
      description = "The routes of one family in one routing table, as ip -json route show prints them.";
      command = [
        ip
        "-json"
        "-family"
        "{family}"
        "route"
        "show"
        "table"
        "{table}"
      ];
      parameters = {
        inherit family;
        table = {
          type = "pattern";
          regex = "^(main|local|default|all|[0-9]+)$";
          default = "main";
          description = "Routing table: main, local, default, all or a table's number.";
        };
      };
    };
    # The kernel's routing policy rules, which pick the table a packet's route comes from: VRFs'
    # and policy routing's among them. ip lists one family's.
    ip-rules = reader // {
      description = "The routing policy rules of one family, as ip -json rule show prints them: which table the kernel looks a packet's route up in, in the order of their priorities.";
      command = [
        ip
        "-json"
        "-family"
        "{family}"
        "rule"
        "show"
      ];
      parameters = { inherit family; };
    };
    route-get = reader // {
      description = "The route the kernel takes to an address.";
      command = [
        ip
        "-json"
        "route"
        "get"
        "{address}"
      ];
      parameters.address = address // {
        description = "IPv4 or IPv6 address, without a zone: ip route get takes none, and finds a link-local address's interface by the routes.";
      };
    };
    neighbours = reader // {
      description = "The neighbour table of IPv4 ARP and IPv6 neighbour discovery, as ip -json neighbour show prints it.";
      command = [
        ip
        "-json"
        "neighbour"
        "show"
      ];
    };
    # ping ends after count replies or 10 seconds, whichever comes first.
    ping = {
      description = "Sends ICMP echo requests to an address and prints the replies, for at most 10 seconds.";
      command = [
        "${pkgs.iputils}/bin/ping"
        "-n"
        "-w"
        "10"
        "-c"
        "{count}"
        "{address}"
      ];
      parameters = {
        address = address // {
          description = "IPv4 or IPv6 address to ping.";
        };
        count = {
          type = "int";
          min = 1;
          max = 10;
          default = "4";
          description = "Number of echo requests.";
        };
      };
      role = "operator";
      timeout = "20s";
      capabilities = [ "CAP_NET_RAW" ];
    };
    # drill takes the server and the type anywhere after the name, and asks the resolvers of
    # /etc/resolv.conf without a server.
    dns-query = {
      description = "Looks up a name in DNS, through the node's resolver or the server given.";
      command = [
        "${pkgs.ldns}/bin/drill"
        "{name}"
        "{type}"
        "@{server}"
      ];
      parameters = {
        name = {
          type = "hostname";
          description = "Name to look up.";
        };
        type = {
          type = "enum";
          values = [
            "A"
            "AAAA"
            "CAA"
            "CNAME"
            "DS"
            "DNSKEY"
            "MX"
            "NS"
            "PTR"
            "SOA"
            "SRV"
            "TXT"
          ];
          optional = true;
          description = "Record type; A when left out.";
        };
        server = address // {
          optional = true;
          description = "Address of the DNS server to ask; the node's resolver when left out.";
        };
      };
      role = "operator";
      timeout = "15s";
    };
    # chalkd connects and closes the connection again. A connect without an answer within 5
    # seconds exits with 124, as coreutils' timeout does, and any other failure with 1.
    tcp-connect = {
      description = "Opens a TCP connection to an address and port, closes it again and says whether it connected.";
      command = [
        (lib.getExe pkgs.chalkos.chalkd)
        "tcp-connect"
        "{address}"
        "{port}"
      ];
      parameters = {
        inherit address;
        port = {
          type = "int";
          min = 1;
          max = 65535;
          description = "TCP port.";
        };
      };
      role = "operator";
      timeout = "10s";
    };
    nft-ruleset = {
      description = "The node's nftables ruleset, as nft list ruleset prints it.";
      command = [
        "${pkgs.nftables}/bin/nft"
        "list"
        "ruleset"
      ];
      role = "operator";
      timeout = "10s";
      capabilities = [ "CAP_NET_ADMIN" ];
    };
  };
}
