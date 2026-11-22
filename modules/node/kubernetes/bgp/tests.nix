# The tests of BGP on Kubernetes images: the frr.conf each config renders, with the marker of a
# speaker, and with the firewall the table that marks BFD, the refusals of the kinds' rules, the
# reload a new password takes and the restart of FRR a delivery that adds or removes the speaker
# takes. firewall tells whether the image has the output bfd-firewall.
{ firewall }:
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
  # A test of tests/<name>.yaml whose output bgp renders tests/<name>/bgp and, with the firewall,
  # whose output bfd-firewall renders tests/<name>/bfd-firewall where that exists.
  rendered = name: {
    "kubernetes-bgp-${name}" = {
      config = ./tests + "/${name}.yaml";
      outputs = {
        bgp = ./tests + "/${name}/bgp";
      }
      // (
        let
          bfd = ./tests + "/${name}/bfd-firewall";
        in
        if firewall && builtins.pathExists bfd then { bfd-firewall = bfd; } else { }
      );
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
  # The first speaker restarts frr.service, which then finds the marker and starts FRR; a config
  # without one leaves no marker, and the restart stops FRR.
  kubernetes-bgp-add-speaker = {
    before = ./tests/unnumbered-without-speaker.yaml;
    config = ./tests/unnumbered.yaml;
    outputs.bgp = ./tests/unnumbered/bgp;
    effects = [
      "bgp"
      "bgp-speaker"
    ];
  };
  kubernetes-bgp-remove-speaker = {
    before = ./tests/unnumbered.yaml;
    config = ./tests/unnumbered-without-speaker.yaml;
    outputs.bgp = ./tests/none/bgp;
    effects = [
      "bgp"
      "bgp-speaker"
    ];
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
  # A peer keeps its AS number.
  kubernetes-bgp-new-peer-asn = {
    before = ./tests/unnumbered.yaml;
    config = ./tests/new-peer-asn.yaml;
    refusals = [
      {
        kind = "BGPPeer";
        name = "fabric";
        path = "spec.asn";
        message = "the AS number stays 65000; add a BGPPeer of another name for 65003";
      }
    ];
  };
}
// refused "bad-fields" [
  {
    kind = "BGPPeer";
    name = "empty";
    path = "spec";
    message = "the peer names neither address nor interfaces; give its address, the interfaces of unnumbered sessions, or the address and the one interface its session runs on";
  }
  {
    kind = "BGPPeer";
    name = "link-local";
    path = "spec.address";
    message = "the address fe80::1 is no neighbour's: give a unicast address, neither loopback nor link-local; a neighbour on a link without one takes interfaces alone";
  }
  {
    kind = "BGPPeer";
    name = "dotted";
    path = "spec.interfaces";
    message = "the interface 10.0.0.1 is named like an address, which FRR cannot tell from a neighbour's; rename the interface";
  }
  {
    kind = "BGPPeer";
    name = "password";
    path = "spec.password";
    message = "the Secret's name and key hold letters, digits, '.', '_' and '-' alone, as the names of frr.service's credentials do";
  }
  {
    kind = "BGPSpeaker";
    name = "bgp";
    path = "spec.routerID";
    message = "FRR takes no router ID 0.0.0.0; give the node another one, unique within its AS";
  }
]
// refused "duplicate-address" [
  {
    message = ''BGPPeer/router-enp1s0, BGPPeer/router-enp2s0: spec.address "fdc8:a1b2:c3d4:1::1" is not unique; FRR runs one session per neighbour address; give the neighbour one BGPPeer per address, a different address on each link'';
  }
]
// refused "shared-unnumbered" (
  map
    (name: {
      kind = "BGPPeer";
      inherit name;
      path = "spec";
      message = "fabric1 carries the unnumbered sessions of another BGPPeer too; FRR runs one unnumbered session per interface, so give the neighbour on that link one BGPPeer";
    })
    [
      "fabric"
      "spine"
    ]
)
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
    message = "FRR binds a session to an address to one interface, so a peer with an address names one interface at most; for a neighbour on several links, give each link a BGPPeer of its own with the neighbour's address on that link, a different address on each, or leave the address out for unnumbered sessions";
  }
]
