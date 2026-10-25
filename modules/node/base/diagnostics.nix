# The diagnostics every image declares: the node's addresses, interfaces, routes and neighbours,
# the route to an address, ping, DNS queries, TCP connects and the nftables ruleset. Those that
# send traffic from the node or show its firewall need an operator. Each tool is the smallest that
# does the job, as every image carries them: ip alone rather than iproute2, ldns's drill rather
# than dig, and bash's /dev/tcp for a TCP connect.
{
  chalkosPackages,
  lib,
  pkgs,
  ...
}:
let
  ip = lib.getExe chalkosPackages.iproute2-ip;
  address = {
    type = "address";
    description = "IPv4 or IPv6 address.";
  };
in
{
  chalkos.diagnostics = {
    addresses = {
      description = "The addresses of the node's interfaces, as ip -json address show prints them.";
      command = [
        ip
        "-json"
        "address"
        "show"
      ];
      role = "reader";
      timeout = "10s";
    };
    links = {
      description = "The node's network interfaces and their state, as ip -json link show prints them.";
      command = [
        ip
        "-json"
        "link"
        "show"
      ];
      role = "reader";
      timeout = "10s";
    };
    # With a table, ip lists one family's routes; inet is its own default.
    routes = {
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
        family = {
          type = "enum";
          values = [
            "inet"
            "inet6"
          ];
          default = "inet";
          description = "Address family: inet for IPv4, inet6 for IPv6.";
        };
        table = {
          type = "pattern";
          regex = "^(main|local|default|all|[0-9]+)$";
          default = "main";
          description = "Routing table: main, local, default, all or a table's number.";
        };
      };
      role = "reader";
      timeout = "10s";
    };
    route-get = {
      description = "The route the kernel takes to an address.";
      command = [
        ip
        "-json"
        "route"
        "get"
        "{address}"
      ];
      parameters.address = address;
      role = "reader";
      timeout = "10s";
    };
    neighbours = {
      description = "The neighbour table of IPv4 ARP and IPv6 neighbour discovery, as ip -json neighbour show prints it.";
      command = [
        ip
        "-json"
        "neighbour"
        "show"
      ];
      role = "reader";
      timeout = "10s";
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
    # bash opens the connection and closes it on exit; the address and port are its arguments,
    # never part of the script. timeout ends a connect that would wait for TCP's own timeout.
    tcp-connect = {
      description = "Opens a TCP connection to an address and port, closes it again and says whether it connected.";
      command = [
        "${pkgs.coreutils}/bin/timeout"
        "5"
        "${pkgs.bash}/bin/bash"
        "-c"
        '': <>"/dev/tcp/$1/$2" && echo "connected to $1 port $2"''
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
