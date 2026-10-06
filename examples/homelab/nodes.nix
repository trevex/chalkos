{
  chalkos.nodes = {
    cp1 = {
      role = "controlplane";
      install.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001";
      network.networks."10-uplink" = {
        matchConfig.Name = "enp1s0";
        address = [ "10.0.0.11/24" ];
        gateway = [ "10.0.0.1" ];
      };
      rack.location = "rack-a/u12";
    };

    w1 = {
      role = "worker";
      install.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002";
      labels."node.kubernetes.io/storage" = "ssd";
      taints = [
        {
          key = "dedicated";
          value = "storage";
          effect = "NoSchedule";
        }
      ];
      rack.location = "rack-a/u14";
    };
  };
}
