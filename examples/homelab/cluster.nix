{
  imports = [
    ./nodes.nix
    ./extensions/rack.nix
  ];

  chalkos.cluster = {
    name = "homelab";
    # cp1's address: the API server of the only control-plane node.
    endpoint = "https://10.0.0.11:6443";
  };

  chalkos.roles.controlplane.kubernetes.kind = "controlplane";
  chalkos.roles.worker = {
    # Every worker carries a SATA SSD for Longhorn replicas next to its NVMe system disk.
    storage.volumes.longhorn = {
      disk = {
        model = "Samsung SSD 870*";
        type = "ssd";
      };
      format = "xfs";
      mountPoint = "/var/lib/longhorn";
    };
  };

  chalkos.rack.enable = true;
}
