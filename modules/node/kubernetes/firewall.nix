# The firewall of images that run Kubernetes: the kubelet's and the NodePorts' ports, the control
# plane's ports, which only control planes open, and flannel's VXLAN, accepted only when sent to
# the node's addresses.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  flannel = config.chalkos.cni.provider == "flannel";
  chalkd = lib.getExe chalkosPackages.chalkd;

  # flannel's VXLAN carries pod traffic unauthenticated, so it is accepted only when sent to the
  # node's addresses, which flanneld uses and other nodes send to: on the interface holding one,
  # or on any for one on a loopback or dummy interface. chalkd picks them after the firewall
  # started, so a table of chalkos's own, which chalkd fills (pkg/kubernetes/vxlan), marks such
  # packets, and the firewall accepts what carries the mark. The firewall's reloads leave that
  # table alone.
  vxlanFirewall = flannel && config.networking.firewall.enable;
  # The packet mark bit chalkd's table sets, vxlan.Mark; the vxlan-rule check fails when the two
  # differ.
  vxlanMark = "0x01000000";

  # A packet mark bit of the control plane's ports, which chalkos-controlplane sets; kube-proxy
  # (0x4000, 0x8000), flannel and the VXLAN rule leave it alone. It is cleared on every packet
  # the node receives, so a network plugin of the cluster's own (cni.provider "none") must not
  # use it.
  controlPlaneMark = "0x02000000";
  controlPlanePorts = pkgs.callPackage ../../../nix/controlplane-ports.nix {
    typeCommand = config.chalkos.kinds.Node.get { path = "spec.type"; };
    ports = config.networking.firewall.enable;
    mark = controlPlaneMark;
  };
in
{
  config = lib.mkIf config.chalkos.kubernetes.enable {
    systemd.services.chalkos-kubernetes = lib.mkIf vxlanFirewall {
      after = [ "nftables.service" ];
      path = [ pkgs.nftables ];
      serviceConfig = {
        # No VXLAN is accepted while the addresses are picked, nor when none are found.
        ExecStartPre = [ "${chalkd} vxlan-rule" ];
        # The preparation fills the table last, so the node is marked prepared only with the rules
        # in place, and a failure to fill it is the preparation's.
        ExecStart = "${chalkd} prepare-kubernetes --vxlan-rule";
      };
    };

    systemd.services.chalkos-controlplane = {
      description = "Open the control plane's ports and make etcd's directory by the node's type";
      chalkos.kinds = [ "Node" ];
      wantedBy = [ "multi-user.target" ];
      after = [
        "chalkos-config.service"
        "local-fs.target"
      ]
      ++ lib.optional config.networking.firewall.enable "nftables.service";
      unitConfig.RequiresMountsFor = "/var/lib/etcd";
      before = [
        "chalkos-kubernetes.service"
        "kubelet.service"
      ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe controlPlanePorts;
      };
    };

    networking.firewall = {
      allowedTCPPorts = [ 10250 ];
      allowedTCPPortRanges = [
        {
          from = 30000;
          to = 32767;
        }
      ];
      # Accept what chalkos-controlplane and chalkos-vxlan marked. Their tables clear the marks
      # after this chain, so neither these packets nor those they carry keep them. etcd's
      # clients and peers on the other control-plane nodes need 2379 and 2380; etcd accepts only
      # certificates of its CA there. Known limitation: the ports are open to every source, not
      # only to the cluster's node addresses.
      extraInputRules = lib.concatStringsSep "\n" (
        [
          "tcp dport { 6443, 2379, 2380 } meta mark & ${controlPlaneMark} == ${controlPlaneMark} accept"
        ]
        ++ lib.optional flannel "udp dport 8472 meta mark & ${vxlanMark} == ${vxlanMark} accept"
      );
    };
  };
}
