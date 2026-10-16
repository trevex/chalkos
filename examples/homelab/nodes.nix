{
  chalkos.nodes = {
    cp1 = {
      role = "node";
      kubernetes.kind = "controlplane";
      storage.system.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001";
      network.networks."10-uplink" = {
        matchConfig.Name = "enp1s0";
        address = [ "10.0.0.11/24" ];
        gateway = [ "10.0.0.1" ];
      };
      rack.location = "rack-a/u12";
    };

    w1 = {
      role = "node";
      storage = {
        system.disk = "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002";
        var.size = "200G";
        # A SATA SSD for Longhorn replicas next to the NVMe system disk.
        volumes.longhorn = {
          disk = {
            model = "Samsung SSD 870*";
            type = "ssd";
          };
          format = "xfs";
          mountPoint = "/var/lib/longhorn";
        };
      };
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
