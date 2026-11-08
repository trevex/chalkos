# The validator bgp-peers: it reads the node's Peer and Network documents, by kind, and
# refuses a Peer whose IPv4 address lies on none of the subnets of the node's static IPv4
# addresses. bird.conf sets no multihop, so bird talks to a neighbour on a link of its own alone,
# and a session with a neighbour elsewhere would never come up. The subnet of a link that takes
# its address by DHCP is unknown, so when any Network has no static IPv4 address or enables
# DHCPv4, every Peer passes.

# An IPv4 address as a number.
def number: split(".") | map(tonumber) | .[0] * 16777216 + .[1] * 65536 + .[2] * 256 + .[3];

# Whether the address, the input, lies in the subnet of the address with prefix length, such as
# 10.0.0.11/24.
def within($cidr):
  ($cidr | split("/")) as [$address, $length]
  | pow(2; 32 - ($length | tonumber)) as $size
  | (number / $size | floor) == ($address | number / $size | floor);

def ipv4: test("^[0-9]+(\\.[0-9]+){3}$");

# A value as a list: a section or key of a networkd document is one value or a list of them.
def each: if type == "array" then .[] else . end;

# The static IPv4 addresses of a Network, each with its prefix length: those of its [Network]
# sections and of its [Address] sections. An address without one is a host address, /32;
# 0.0.0.0 asks networkd to pick an unused range, so it names no subnet.
def addresses:
  [ (.spec.network? // [] | each | .address? // [] | each),
    (.spec.address? // [] | each | .address? // [] | each)
    | select(type == "string")
    | if test("/") then . else . + "/32" end
    | select(test("^[0-9]+(\\.[0-9]+){3}/[0-9]+$") and (startswith("0.0.0.0/") | not)) ];

def dhcpv4:
  [.spec.network? // [] | each | .dhcp? | select(. == "yes" or . == "ipv4" or . == true)] != [];

.documents.Network as $networks
| [ $networks[] | addresses[] ] as $subnets
| { refusals: [
    if $networks == [] or ($networks | any(addresses == [] or dhcpv4)) then empty else
      .documents.Peer[] | select(.spec.address | ipv4)
      | select(.spec.address as $peer | [$subnets[] | . as $s | $peer | within($s)] | any | not)
      | { kind: "Peer", name: .metadata.name, path: "spec.address",
          message: "\(.spec.address) lies on no subnet of the node's addresses, \($subnets | join(", ")); bird reaches a neighbour on a link of its own alone" }
    end
  ] }
