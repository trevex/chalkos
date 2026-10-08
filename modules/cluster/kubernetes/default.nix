# The Kubernetes objects every cluster gets: RBAC for chalkos's identities and kube-proxy.
# Features add theirs to the same list; the control plane applies it before
# chalkos.cluster.manifests.
{ config, lib, ... }:
let
  k = config.chalkos.cluster.kubernetes;
  clusterRoleBinding = name: role: subject: {
    apiVersion = "rbac.authorization.k8s.io/v1";
    kind = "ClusterRoleBinding";
    metadata.name = name;
    roleRef = {
      apiGroup = "rbac.authorization.k8s.io";
      kind = "ClusterRole";
      name = role;
    };
    subjects = [ subject ];
  };
  group = name: {
    apiGroup = "rbac.authorization.k8s.io";
    kind = "Group";
    inherit name;
  };
  user = name: {
    apiGroup = "rbac.authorization.k8s.io";
    kind = "User";
    inherit name;
  };

  # kube-proxy serves every family the node has addresses of; it binds as kubeadm's does, by the
  # primary family.
  kubeProxyConfig = {
    apiVersion = "kubeproxy.config.k8s.io/v1alpha1";
    kind = "KubeProxyConfiguration";
    # The node's firewall and flannel run on nftables too.
    mode = "nftables";
    clusterCIDR = lib.concatMapStringsSep "," (family: k.podCIDRs.${family}) k.ipFamilies;
    bindAddress =
      {
        ipv4 = "0.0.0.0";
        ipv6 = "::";
      }
      .${builtins.head k.ipFamilies};
    # NodePorts answer on the node's own addresses, one per family, as the kubelet registered them.
    nodePortAddresses = [ "primary" ];
    clientConnection.kubeconfig = "/var/lib/kube-proxy/kubeconfig.conf";
  };
  # Pods reach the API server at the cluster endpoint: the service address needs kube-proxy.
  kubeProxyKubeconfig = {
    apiVersion = "v1";
    kind = "Config";
    clusters = [
      {
        name = "default";
        cluster = {
          certificate-authority = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt";
          server = config.chalkos.cluster.endpoint;
        };
      }
    ];
    contexts = [
      {
        name = "default";
        context = {
          cluster = "default";
          namespace = "default";
          user = "default";
        };
      }
    ];
    current-context = "default";
    users = [
      {
        name = "default";
        user.tokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token";
      }
    ];
  };

  kubeProxy = [
    {
      apiVersion = "v1";
      kind = "ServiceAccount";
      metadata = {
        name = "kube-proxy";
        namespace = "kube-system";
      };
    }
    (clusterRoleBinding "chalkos:node-proxier" "system:node-proxier" {
      kind = "ServiceAccount";
      name = "kube-proxy";
      namespace = "kube-system";
    })
    {
      apiVersion = "v1";
      kind = "ConfigMap";
      metadata = {
        name = "kube-proxy";
        namespace = "kube-system";
        labels.app = "kube-proxy";
      };
      data = {
        "kubeconfig.conf" = builtins.toJSON kubeProxyKubeconfig;
        "config.conf" = builtins.toJSON kubeProxyConfig;
      };
    }
    {
      apiVersion = "apps/v1";
      kind = "DaemonSet";
      metadata = {
        name = "kube-proxy";
        namespace = "kube-system";
        labels.k8s-app = "kube-proxy";
      };
      spec = {
        selector.matchLabels.k8s-app = "kube-proxy";
        updateStrategy.type = "RollingUpdate";
        template = {
          metadata.labels.k8s-app = "kube-proxy";
          spec = {
            priorityClassName = "system-node-critical";
            hostNetwork = true;
            serviceAccountName = "kube-proxy";
            nodeSelector."kubernetes.io/os" = "linux";
            tolerations = [ { operator = "Exists"; } ];
            containers = [
              {
                name = "kube-proxy";
                image = "registry.k8s.io/kube-proxy:v${k.package.version}";
                command = [
                  "/usr/local/bin/kube-proxy"
                  "--config=/var/lib/kube-proxy/config.conf"
                  "--hostname-override=$(NODE_NAME)"
                ];
                env = [
                  {
                    name = "NODE_NAME";
                    valueFrom.fieldRef.fieldPath = "spec.nodeName";
                  }
                ];
                securityContext.privileged = true;
                volumeMounts = [
                  {
                    name = "kube-proxy";
                    mountPath = "/var/lib/kube-proxy";
                  }
                  {
                    name = "xtables-lock";
                    mountPath = "/run/xtables.lock";
                  }
                  {
                    name = "lib-modules";
                    mountPath = "/lib/modules";
                    readOnly = true;
                  }
                ];
              }
            ];
            volumes = [
              {
                name = "kube-proxy";
                configMap.name = "kube-proxy";
              }
              {
                name = "xtables-lock";
                hostPath = {
                  path = "/run/xtables.lock";
                  type = "FileOrCreate";
                };
              }
              {
                # NixOS keeps the running kernel's modules here, not in /lib/modules.
                name = "lib-modules";
                hostPath.path = "/run/booted-system/kernel-modules/lib/modules";
              }
            ];
          };
        };
      };
    }
  ];
in
{
  options.chalkos.cluster.kubernetes.addons = lib.mkOption {
    type = lib.types.listOf (lib.types.attrsOf lib.types.anything);
    internal = true;
    description = ''
      Built-in Kubernetes objects: these and those of features, applied before
      chalkos.cluster.manifests. Ordered with lib.mkOrder: RBAC 100, kube-proxy 200, CNI 300,
      DNS 400.
    '';
  };

  config.chalkos.cluster.kubernetes.addons = lib.mkMerge [
    (lib.mkOrder 100 [
      # Admin kubeconfigs from chalkctl kubeconfig carry this group, which RBAC can revoke,
      # unlike system:masters.
      (clusterRoleBinding "chalkos:cluster-admins" "cluster-admin" (group "chalkos:cluster-admins"))
      # Kubelets renew their client certificates; the controller-manager approves the requests
      # of nodes renewing their own.
      (clusterRoleBinding "chalkos:selfnodeclient"
        "system:certificates.k8s.io:certificatesigningrequests:selfnodeclient"
        (group "system:nodes")
      )
      # The API server reaches kubelets for logs, exec and port forwarding.
      (clusterRoleBinding "chalkos:apiserver-kubelet" "system:kubelet-api-admin" (
        user "chalkos:kube-apiserver-kubelet-client"
      ))
    ])
    (lib.mkOrder 200 kubeProxy)
  ];
}
