# A cluster of one control plane and one worker, which chalklab runs as QEMU virtual machines on
# this machine. Each VM has two network cards: one on the lab network, 192.168.123.0/24, between
# the VMs, which chalklab connects by the MAC address the node's network matches; and one that
# reaches the internet and the ports chalklab forwards from 127.0.0.1, configured by DHCP.
let
  labNetwork = n: {
    networks."10-lab" = {
      matchConfig.MACAddress = "52:54:00:7b:00:${n}";
      address = [ "192.168.123.${n}/24" ];
    };
  };
in
{
  chalkos.cluster = {
    name = "lab";
    # cp1 on the lab network: the API server of the only control plane.
    endpoint = "https://192.168.123.11:6443";
    # The public part of the cluster's secrets, which chalkctl gen secrets writes. The images
    # carry its OS CA.
    osCA = ./secrets.pub.json;
  };

  # One image for both nodes; cp1's kind makes it the control plane.
  chalkos.roles.node = { };

  chalkos.nodes = {
    cp1 = {
      role = "node";
      platform = "kvm";
      kubernetes.kind = "controlplane";
      storage.system.disk = "/dev/vda";
      network = labNetwork "11";
    };
    w1 = {
      role = "node";
      platform = "kvm";
      storage.system.disk = "/dev/vda";
      network = labNetwork "12";
    };
  };
}
