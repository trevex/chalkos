# The tests of BGP on Kubernetes images: the frr.conf each config renders, the refusals of the
# kinds' rules and the reload a new password takes.
let
  # A test of tests/<name>.yaml with the refusals given.
  refused = name: refusals: {
    "kubernetes-bgp-${name}" = {
      config = ./tests + "/${name}.yaml";
      inherit refusals;
    };
  };
  unconfigured = i: what: "${i} has no Network document whose [Match] Name= names it; ${what}";
  sessions = "FRR runs sessions on interfaces the node configures";
  # A test of tests/<name>.yaml whose output bgp renders tests/<name>/bgp.
  rendered = name: {
    "kubernetes-bgp-${name}" = {
      config = ./tests + "/${name}.yaml";
      outputs.bgp = ./tests + "/${name}/bgp";
    };
  };
in
# The frr.conf of each example, which vtysh's check passes.
rendered "unnumbered"
// rendered "address-interface"
// rendered "address"
// rendered "mixed"
// rendered "none"
// {
  # A new password leaves frr.conf as it was, which names the credential alone, and FRR reloads to
  # load the credential anew.
  kubernetes-bgp-new-password = {
    before = ./tests/address-interface.yaml;
    config = ./tests/address-interface-new-password.yaml;
    outputs.bgp = ./tests/address-interface/bgp;
    effects = [ "bgp" ];
  };
  # A speaker keeps its AS number.
  kubernetes-bgp-new-asn = {
    before = ./tests/unnumbered.yaml;
    config = ./tests/new-asn.yaml;
    refusals = [
      {
        kind = "BGPSpeaker";
        name = "bgp";
        path = "spec.asn";
        message = "the AS number stays 65001; to change it, deliver the config without the BGPSpeaker, which ends the node's sessions, and then with the new AS number";
      }
    ];
  };
}
// refused "no-router-id" [
  {
    kind = "BGPSpeaker";
    name = "bgp";
    path = "spec";
    message = "the Node's nodeIP.addresses hold no IPv4 address, which FRR would take as its router ID; give routerID, an IPv4 address unique within the node's AS, such as 0.0.0.11";
  }
]
// refused "unconfigured-peer-interface" [
  {
    kind = "BGPPeer";
    name = "fabric";
    path = "spec.interfaces";
    message = unconfigured "fabric2" sessions;
  }
]
// refused "unconfigured-export" [
  {
    kind = "BGPSpeaker";
    name = "bgp";
    path = "spec.export.interfaces";
    message = unconfigured "node0" "FRR announces the addresses of interfaces the node configures";
  }
]
// refused "match-glob" [
  {
    kind = "BGPPeer";
    name = "fabric";
    path = "spec.interfaces";
    message = unconfigured "fabric0" sessions;
  }
]
// refused "peer-without-speaker" [
  {
    kind = "BGPPeer";
    name = "fabric";
    path = "spec";
    message = "a node with BGPPeer documents needs a BGPSpeaker document, which gives FRR its AS number and router ID";
  }
]
// refused "address-two-interfaces" [
  {
    kind = "BGPPeer";
    name = "router";
    path = "spec";
    message = "FRR runs one session per neighbour address, so a peer with an address names one interface at most, the one its session is bound to; give each interface a BGPPeer of its own with the neighbour's address on that link, or leave the address out for unnumbered sessions";
  }
]
