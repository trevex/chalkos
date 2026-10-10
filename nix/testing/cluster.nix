# The cluster whose images the e2e tests boot, its secrets, and manifests the tests deliver with
# chalkctl. Each role has a node standing for the VM that runs it.
{ self, pkgs }:
let
  secrets = import ./secrets.nix { inherit pkgs; };
  # The registry the Kubernetes test serves on the host, as the VMs reach it through their
  # user-mode NIC.
  registry = "http://10.0.2.100:5000";
  # The Kubernetes nodes' network between the VMs, on their second NIC: 192.168.100.0/24 and
  # fd00:100::/64, with host n at .n and ::n. It has no router.
  clusterNetwork = mac: n: {
    networks."10-cluster" = {
      matchConfig.MACAddress = mac;
      address = [
        "192.168.100.${n}/24"
        "fd00:100::${n}/64"
      ];
    };
  };
  definition = {
    chalkos.cluster = {
      name = "chalklab";
      endpoint = "https://192.168.100.11:6443";
      osCA = "${secrets}/secrets.pub.json";
      kubernetes = {
        allowSchedulingOnControlPlanes = true;
        ipFamilies = [
          "ipv4"
          "ipv6"
        ];
        # The cluster network and w1's routed addresses.
        vxlanSourceSubnets = [
          "192.168.100.0/24"
          "fd00:100::/64"
          "192.168.200.0/24"
          "fd00:200::/64"
        ];
      };
    };
    # The tests answer NTP on the host, which the user-mode NIC reaches at 10.0.2.2, without NTS.
    chalkos.time.servers = [
      {
        host = "10.0.2.2";
        nts = false;
        port = 12300;
      }
    ];
    # w1's addresses are on a dummy interface, whose MTU flannel would take: the cluster network's
    # 1500 less IPv6's VXLAN overhead of 70.
    chalkos.cni.flannel.mtu = 1430;
    # The default layout: VAR fills the system disk.
    chalkos.roles.test = {
      kubernetes.kind = null;
      nixosModules = [ ../../modules/testing/test-image.nix ];
    };
    chalkos.nodes.chalklab = {
      role = "test";
      storage.system.disk = "/dev/vda";
    };
    # A node the installer installs onto the disk with this serial.
    chalkos.nodes.chalklab-target = {
      role = "test";
      storage.system.disk.serial = "chalk-target";
    };
    # A fixed VAR, an unencrypted and a raw volume next to it, and a volume on a second disk.
    chalkos.roles.storage = {
      kubernetes.kind = null;
      nixosModules = [ ../../modules/testing/test-image.nix ];
      storage = {
        var.size = "2G";
        volumes = {
          plain = {
            size = "256M";
            mountPoint = "/srv/plain";
            encryption.mode = "none";
          };
          raw = {
            size = "128M";
            format = null;
          };
          data = {
            disk.serial = "chalk-data";
            format = "xfs";
            mountPoint = "/var/lib/data";
          };
        };
      };
    };
    chalkos.nodes.chalklab-storage = {
      role = "storage";
      storage.system.disk = "/dev/vda";
    };
    # A control plane and a worker on KVM, connected through the switch of the Kubernetes test.
    chalkos.roles.k8s-controlplane = {
      kubernetes.kind = "controlplane";
      nixosModules = [ ../../modules/testing/test-image.nix ];
    };
    chalkos.roles.k8s-worker = {
      kubernetes.kind = "worker";
      nixosModules = [ ../../modules/testing/test-image.nix ];
    };
    # cp1 reaches w1's addresses through w1's address on the cluster network, as a routing daemon
    # would install the routes.
    chalkos.nodes.cp1 = {
      role = "k8s-controlplane";
      platform = "kvm";
      storage.system.disk = "/dev/vda";
      network = pkgs.lib.recursiveUpdate (clusterNetwork "52:54:00:00:01:11" "11") {
        networks."10-cluster".routes = [
          {
            Destination = "192.168.200.12/32";
            Gateway = "192.168.100.12";
          }
          {
            Destination = "fd00:200::12/128";
            Gateway = "fd00:100::12";
          }
        ];
      };
    };
    # w1's addresses are on a dummy interface, as a routing daemon announces them; it picks them
    # at boot, neither its user-mode NIC's nor its cluster network's.
    chalkos.nodes.w1 = {
      role = "k8s-worker";
      platform = "kvm";
      storage.system.disk = "/dev/vda";
      network = pkgs.lib.recursiveUpdate (clusterNetwork "52:54:00:00:01:12" "12") {
        netdevs."20-bgp0".netdevConfig = {
          Kind = "dummy";
          Name = "bgp0";
          MTUBytes = "1500";
        };
        networks."20-bgp0" = {
          matchConfig.Name = "bgp0";
          address = [
            "192.168.200.12/32"
            "fd00:200::12/128"
          ];
        };
      };
      kubernetes.validSubnets = [
        "192.168.200.0/24"
        "fd00:200::/64"
      ];
    };
  };
  # Three control planes of an IPv6-only cluster behind the VIP fd00:100::10, connected through
  # the switch of the HA test. The definition is a cluster of its own: the endpoint and the VIP
  # are in the images.
  haDefinition = {
    chalkos.cluster = {
      name = "chalklab-ha";
      endpoint = "https://[fd00:100::10]:6443";
      osCA = "${secrets}/secrets.pub.json";
      kubernetes = {
        allowSchedulingOnControlPlanes = true;
        ipFamilies = [ "ipv6" ];
        vip.addresses = [ "fd00:100::10" ];
        # The test boots a pinned node without its address; it gives up after this.
        nodeIP.timeout = 30;
      };
    };
    chalkos.roles.k8s-ha = {
      kubernetes.kind = "controlplane";
      nixosModules = [ ../../modules/testing/test-image.nix ];
    };
    chalkos.nodes = pkgs.lib.genAttrs [ "cp1" "cp2" "cp3" ] (
      name:
      let
        n = pkgs.lib.removePrefix "cp" name;
      in
      {
        role = "k8s-ha";
        storage.system.disk = "/dev/vda";
        network = clusterNetwork "52:54:00:00:02:1${n}" "1${n}";
      }
    );
  };
  # The Kubernetes nodes pull every image through the test's registry.
  mirrored = {
    chalkos.cluster.registries = {
      # The test's registry serves plain HTTP inside the sandbox.
      allowPlainHTTP = true;
      mirrors = {
        "registry.k8s.io" = [ registry ];
        "ghcr.io" = [ registry ];
        "docker.io" = [ registry ];
      };
    };
  };
  manifestOf =
    name: modules:
    pkgs.writeText "${name}.json" (
      builtins.toJSON (self.lib.mkCluster { modules = [ definition ] ++ modules; }).manifest
    );
  cluster = self.lib.mkCluster {
    modules = [
      definition
      mirrored
    ];
  };
  haCluster = self.lib.mkCluster {
    modules = [
      haDefinition
      mirrored
    ];
  };
  # The metal image of a role again, at another version and with the modules given.
  imageAt =
    modules: role: version: extra:
    (self.lib.mkCluster {
      modules = modules ++ [
        { chalkos.roles.${role}.nixosModules = [ ({ system.image.version = version; } // extra) ]; }
      ];
    }).roles.${role}.images.metal;
in
{
  inherit cluster haCluster;
  # The test image at the versions e2e-upgrade installs: 0.2.0, and 0.3.0, which never becomes
  # healthy, as a unit fails at every boot the way a broken service would, and falls back after
  # one try of 30 seconds.
  upgradeImage = imageAt [
    definition
    mirrored
  ] "test" "0.2.0" { };
  unhealthyImage =
    imageAt
      [
        definition
        mirrored
      ]
      "test"
      "0.3.0"
      {
        chalkos.upgrade = {
          bootTries = 1;
          healthTimeout = 30;
        };
        systemd.services.chalktest-broken = {
          description = "Fail, as a broken service does";
          wantedBy = [ "multi-user.target" ];
          serviceConfig = {
            Type = "oneshot";
            ExecStart = "${pkgs.coreutils}/bin/false";
          };
        };
      };
  # The HA test's image at the version its rolling upgrade installs.
  haUpgradeImage = imageAt [
    haDefinition
    mirrored
  ] "k8s-ha" "0.2.0" { };
  kubernetesImages = import ./kubernetes-images.nix {
    inherit pkgs;
    kubernetesVersion = cluster.cluster.kubernetes.package.version;
  };
  inherit secrets;
  manifests = pkgs.linkFarm "chalkos-test-manifests" {
    "base.json" = manifestOf "base" [ ];
    # A new encrypted volume on an empty disk: an additive change.
    "added.json" = manifestOf "added" [
      {
        chalkos.nodes.chalklab.storage.volumes.extra = {
          disk.serial = "chalk-extra";
          mountPoint = "/srv/extra";
        };
      }
    ];
    # VAR with a fixed size where it filled its disk: a destructive change.
    "destructive.json" = manifestOf "destructive" [
      { chalkos.nodes.chalklab.storage.var.size = "1G"; }
    ];
    "ha.json" = pkgs.writeText "ha.json" (builtins.toJSON haCluster.manifest);
  };
}
