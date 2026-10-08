# The pod network. flannel connects pods across nodes with VXLAN; none leaves the choice to the
# cluster's own manifests.
{ config, lib, ... }:
let
  cfg = config.chalkos.cni;
  k = config.chalkos.cluster.kubernetes;
  inUse = family: lib.elem family k.ipFamilies;
  # VXLAN in every family of the cluster (flannel.1 and flannel-v6.1, both on UDP 8472), with the
  # rules flannel needs in nftables tables of its own.
  netConf = {
    EnableNFTables = true;
    Backend = {
      Type = "vxlan";
    }
    // lib.optionalAttrs (cfg.flannel.mtu != null) { MTU = cfg.flannel.mtu; };
  }
  // lib.optionalAttrs (inUse "ipv4") { Network = k.podCIDRs.ipv4; }
  // lib.optionalAttrs (!inUse "ipv4") { EnableIPv4 = false; }
  // lib.optionalAttrs (inUse "ipv6") {
    EnableIPv6 = true;
    IPv6Network = k.podCIDRs.ipv6;
  };
  # flanneld uses exactly the node's addresses, one per family, as the kubelet registered them and
  # reports them as the pod's addresses: the firewall accepts VXLAN to those alone. With them as
  # public addresses flanneld finds the interface holding them.
  flanneld = ''
    set -- --ip-masq --kube-subnet-mgr --healthz-port=8081
    IFS=,
    for ip in $POD_IPS; do
      case $ip in
        *:*) set -- "$@" "--public-ipv6=$ip" ;;
        *) set -- "$@" "--public-ip=$ip" ;;
      esac
    done
    exec /opt/bin/flanneld "$@"
  '';
  labels = {
    tier = "node";
    app = "flannel";
    k8s-app = "flannel";
  };
  flannel = [
    {
      apiVersion = "v1";
      kind = "Namespace";
      metadata = {
        name = "kube-flannel";
        labels = {
          k8s-app = "flannel";
          "pod-security.kubernetes.io/enforce" = "privileged";
        };
      };
    }
    {
      apiVersion = "rbac.authorization.k8s.io/v1";
      kind = "ClusterRole";
      metadata = {
        name = "flannel";
        labels.k8s-app = "flannel";
      };
      rules = [
        {
          apiGroups = [ "" ];
          resources = [ "pods" ];
          verbs = [ "get" ];
        }
        {
          apiGroups = [ "" ];
          resources = [ "nodes" ];
          verbs = [
            "get"
            "list"
            "watch"
          ];
        }
        {
          apiGroups = [ "" ];
          resources = [ "nodes/status" ];
          verbs = [ "patch" ];
        }
      ];
    }
    {
      apiVersion = "rbac.authorization.k8s.io/v1";
      kind = "ClusterRoleBinding";
      metadata = {
        name = "flannel";
        labels.k8s-app = "flannel";
      };
      roleRef = {
        apiGroup = "rbac.authorization.k8s.io";
        kind = "ClusterRole";
        name = "flannel";
      };
      subjects = [
        {
          kind = "ServiceAccount";
          name = "flannel";
          namespace = "kube-flannel";
        }
      ];
    }
    {
      apiVersion = "v1";
      kind = "ServiceAccount";
      metadata = {
        name = "flannel";
        namespace = "kube-flannel";
        labels.k8s-app = "flannel";
      };
    }
    {
      apiVersion = "v1";
      kind = "ConfigMap";
      metadata = {
        name = "kube-flannel-cfg";
        namespace = "kube-flannel";
        inherit labels;
      };
      data = {
        "cni-conf.json" = builtins.toJSON {
          name = "cbr0";
          cniVersion = "0.3.1";
          plugins = [
            {
              type = "flannel";
              delegate = {
                hairpinMode = true;
                isDefaultGateway = true;
              };
            }
            {
              type = "portmap";
              capabilities.portMappings = true;
              backend = "nftables";
            }
          ];
        };
        "net-conf.json" = builtins.toJSON netConf;
      };
    }
    {
      apiVersion = "apps/v1";
      kind = "DaemonSet";
      metadata = {
        name = "kube-flannel-ds";
        namespace = "kube-flannel";
        inherit labels;
      };
      spec = {
        selector.matchLabels.app = "flannel";
        template = {
          metadata.labels = labels;
          spec = {
            hostNetwork = true;
            priorityClassName = "system-node-critical";
            serviceAccountName = "flannel";
            nodeSelector."kubernetes.io/os" = "linux";
            tolerations = [
              {
                operator = "Exists";
                effect = "NoSchedule";
              }
            ];
            # The flannel CNI plugin comes with the image (containerd's bin_dirs), so only the
            # network configuration is installed on the host.
            initContainers = [
              {
                name = "install-cni";
                image = cfg.flannel.image;
                command = [ "/opt/bin/install-conf" ];
                args = [
                  "/etc/kube-flannel/cni-conf.json"
                  "/etc/cni/net.d/10-flannel.conflist"
                ];
                volumeMounts = [
                  {
                    name = "cni";
                    mountPath = "/etc/cni/net.d";
                  }
                  {
                    name = "flannel-cfg";
                    mountPath = "/etc/kube-flannel/";
                  }
                ];
              }
            ];
            containers = [
              {
                name = "kube-flannel";
                image = cfg.flannel.image;
                command = [
                  "/bin/sh"
                  "-c"
                  flanneld
                ];
                ports = [
                  {
                    name = "healthz";
                    containerPort = 8081;
                    protocol = "TCP";
                  }
                ];
                livenessProbe = {
                  httpGet = {
                    path = "/healthz";
                    port = "healthz";
                  };
                  initialDelaySeconds = 10;
                  periodSeconds = 30;
                };
                readinessProbe = {
                  httpGet = {
                    path = "/readyz";
                    port = "healthz";
                  };
                  initialDelaySeconds = 5;
                  periodSeconds = 10;
                };
                resources.requests = {
                  cpu = "100m";
                  memory = "50Mi";
                };
                securityContext = {
                  privileged = false;
                  capabilities.add = [
                    "NET_ADMIN"
                    "NET_RAW"
                  ];
                };
                env = [
                  {
                    name = "POD_NAME";
                    valueFrom.fieldRef.fieldPath = "metadata.name";
                  }
                  {
                    name = "POD_NAMESPACE";
                    valueFrom.fieldRef.fieldPath = "metadata.namespace";
                  }
                  {
                    # The node's addresses, the primary family's first, separated by commas.
                    name = "POD_IPS";
                    valueFrom.fieldRef.fieldPath = "status.podIPs";
                  }
                  {
                    name = "EVENT_QUEUE_DEPTH";
                    value = "5000";
                  }
                  {
                    name = "CONT_WHEN_CACHE_NOT_READY";
                    value = "false";
                  }
                ];
                volumeMounts = [
                  {
                    name = "run";
                    mountPath = "/run/flannel";
                  }
                  {
                    name = "flannel-cfg";
                    mountPath = "/etc/kube-flannel/";
                  }
                  {
                    name = "xtables-lock";
                    mountPath = "/run/xtables.lock";
                  }
                ];
              }
            ];
            volumes = [
              {
                name = "run";
                hostPath.path = "/run/flannel";
              }
              {
                # containerd reads CNI configurations from VAR; /etc is read-only.
                name = "cni";
                hostPath.path = "/var/lib/cni/net.d";
              }
              {
                name = "flannel-cfg";
                configMap.name = "kube-flannel-cfg";
              }
              {
                name = "xtables-lock";
                hostPath = {
                  path = "/run/xtables.lock";
                  type = "FileOrCreate";
                };
              }
            ];
          };
        };
      };
    }
  ];
in
{
  options.chalkos.cni = {
    provider = lib.mkOption {
      type = lib.types.enum [
        "flannel"
        "none"
      ];
      default = "flannel";
      description = ''
        Pod network. `flannel` connects pods across nodes with VXLAN; `none` installs no
        network, for clusters whose manifests bring their own.
      '';
    };
    flannel.image = lib.mkOption {
      type = lib.types.str;
      default = "ghcr.io/flannel-io/flannel:v0.28.9";
      description = "Image of flannel.";
    };
    flannel.mtu = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      example = 1430;
      description = ''
        MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN devices and the
        pods get 50 less. null takes the MTU of the interface holding the node's address. Nodes
        whose address is on a loopback interface need it set, and a dummy interface holding a
        node's address needs an MTU of at least this, 20 more for an IPv6 address: the nodes
        refuse to prepare otherwise. flannel subtracts 50 in both families but IPv6 VXLAN needs
        70, so with IPv6 in use set this to the network's MTU minus 20; otherwise IPv6 pod packets
        near the full size rely on path MTU discovery.
      '';
    };
  };

  config.chalkos.cluster.kubernetes.addons = lib.mkIf (cfg.provider == "flannel") (
    lib.mkOrder 300 flannel
  );
}
