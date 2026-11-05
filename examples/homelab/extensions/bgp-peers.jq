# The validator bgp-peers: it reads the node's Peer and Network documents, as one array, and
# refuses a Peer whose IPv4 address lies on none of the subnets of the node's static IPv4
# addresses. bird.conf sets no multihop, so bird talks to a neighbour on a link of its own alone,
# and a session with a neighbour elsewhere would never come up. A node without a static IPv4
# address, such as one that takes its address by DHCP, passes: its subnet is unknown.

# An IPv4 address as a number.
def number: split(".") | map(tonumber) | .[0] * 16777216 + .[1] * 65536 + .[2] * 256 + .[3];

# Whether the address, the input, lies in the subnet of the address with prefix length, such as
# 10.0.0.11/24.
def within($cidr):
  ($cidr | split("/")) as [$address, $length]
  | pow(2; 32 - ($length | tonumber)) as $size
  | (number / $size | floor) == ($address | number / $size | floor);

def ipv4: test("^[0-9]+(\\.[0-9]+){3}$");

[ .[] | select(.kind == "Network") | .spec.network.address? // []
  | if type == "array" then .[] else . end
  | select(split("/")[0] | ipv4) ] as $subnets
| { refusals: [
    if $subnets == [] then empty else
      .[] | select(.kind == "Peer" and (.spec.address | ipv4))
      | select(.spec.address as $peer | [$subnets[] | . as $s | $peer | within($s)] | any | not)
      | { kind: "Peer", name: .metadata.name, path: "spec.address",
          message: "\(.spec.address) lies on no subnet of the node's addresses, \($subnets | join(", ")); bird reaches a neighbour on a link of its own alone" }
    end
  ] }
