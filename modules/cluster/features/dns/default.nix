# The cluster DNS: CoreDNS answers for the cluster domain at the service address dnsIP and
# forwards other names to the nodes' resolvers.
{ config, lib, ... }:
let
  k = config.chalkos.cluster.kubernetes;
  labels.k8s-app = "kube-dns";
  corefile = ''
    .:53 {
        errors
        health {
           lameduck 5s
        }
        ready
        kubernetes ${k.domain} in-addr.arpa ip6.arpa {
           pods insecure
           fallthrough in-addr.arpa ip6.arpa
           ttl 30
        }
        prometheus :9153
        forward . /etc/resolv.conf {
           max_concurrent 1000
        }
        cache 30 {
           disable success ${k.domain}
           disable denial ${k.domain}
        }
        loop
        reload
        loadbalance
    }
  '';
  coredns = [
    {
      apiVersion = "v1";
      kind = "ServiceAccount";
      metadata = {
        name = "coredns";
        namespace = "kube-system";
      };
    }
    {
      apiVersion = "rbac.authorization.k8s.io/v1";
      kind = "ClusterRole";
      metadata.name = "system:coredns";
      rules = [
        {
          apiGroups = [ "" ];
          resources = [
            "endpoints"
            "services"
            "pods"
            "namespaces"
          ];
          verbs = [
            "list"
            "watch"
          ];
        }
        {
          apiGroups = [ "discovery.k8s.io" ];
          resources = [ "endpointslices" ];
          verbs = [
            "list"
            "watch"
          ];
        }
      ];
    }
    {
      apiVersion = "rbac.authorization.k8s.io/v1";
      kind = "ClusterRoleBinding";
      metadata.name = "system:coredns";
      roleRef = {
        apiGroup = "rbac.authorization.k8s.io";
        kind = "ClusterRole";
        name = "system:coredns";
      };
      subjects = [
        {
          kind = "ServiceAccount";
          name = "coredns";
          namespace = "kube-system";
        }
      ];
    }
    {
      apiVersion = "v1";
      kind = "ConfigMap";
      metadata = {
        name = "coredns";
        namespace = "kube-system";
      };
      data.Corefile = corefile;
    }
    {
      apiVersion = "apps/v1";
      kind = "Deployment";
      metadata = {
        name = "coredns";
        namespace = "kube-system";
        inherit labels;
      };
      spec = {
        replicas = 2;
        strategy = {
          type = "RollingUpdate";
          rollingUpdate.maxUnavailable = 1;
        };
        selector.matchLabels = labels;
        template = {
          metadata = { inherit labels; };
          spec = {
            priorityClassName = "system-cluster-critical";
            serviceAccountName = "coredns";
            affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution = [
              {
                weight = 100;
                podAffinityTerm = {
                  labelSelector.matchExpressions = [
                    {
                      key = "k8s-app";
                      operator = "In";
                      values = [ "kube-dns" ];
                    }
                  ];
                  topologyKey = "kubernetes.io/hostname";
                };
              }
            ];
            tolerations = [
              {
                key = "CriticalAddonsOnly";
                operator = "Exists";
              }
              {
                key = "node-role.kubernetes.io/control-plane";
                effect = "NoSchedule";
              }
            ];
            nodeSelector."kubernetes.io/os" = "linux";
            containers = [
              {
                name = "coredns";
                image = k.images.coredns;
                resources = {
                  limits.memory = "170Mi";
                  requests = {
                    cpu = "100m";
                    memory = "70Mi";
                  };
                };
                args = [
                  "-conf"
                  "/etc/coredns/Corefile"
                ];
                volumeMounts = [
                  {
                    name = "config-volume";
                    mountPath = "/etc/coredns";
                    readOnly = true;
                  }
                ];
                ports = [
                  {
                    containerPort = 53;
                    name = "dns";
                    protocol = "UDP";
                  }
                  {
                    containerPort = 53;
                    name = "dns-tcp";
                    protocol = "TCP";
                  }
                  {
                    containerPort = 9153;
                    name = "metrics";
                    protocol = "TCP";
                  }
                  {
                    containerPort = 8080;
                    name = "liveness-probe";
                    protocol = "TCP";
                  }
                  {
                    containerPort = 8181;
                    name = "readiness-probe";
                    protocol = "TCP";
                  }
                ];
                livenessProbe = {
                  httpGet = {
                    path = "/health";
                    port = "liveness-probe";
                    scheme = "HTTP";
                  };
                  initialDelaySeconds = 60;
                  timeoutSeconds = 5;
                  successThreshold = 1;
                  failureThreshold = 5;
                };
                readinessProbe.httpGet = {
                  path = "/ready";
                  port = "readiness-probe";
                  scheme = "HTTP";
                };
                securityContext = {
                  allowPrivilegeEscalation = false;
                  capabilities = {
                    add = [ "NET_BIND_SERVICE" ];
                    drop = [ "ALL" ];
                  };
                  readOnlyRootFilesystem = true;
                };
              }
            ];
            dnsPolicy = "Default";
            volumes = [
              {
                name = "config-volume";
                configMap = {
                  name = "coredns";
                  items = [
                    {
                      key = "Corefile";
                      path = "Corefile";
                    }
                  ];
                };
              }
            ];
          };
        };
      };
    }
    {
      apiVersion = "v1";
      kind = "Service";
      metadata = {
        name = "kube-dns";
        namespace = "kube-system";
        labels = labels // {
          "kubernetes.io/cluster-service" = "true";
          "kubernetes.io/name" = "CoreDNS";
        };
      };
      spec = {
        clusterIP = k.dnsIP;
        selector = labels;
        ports = [
          {
            name = "dns";
            port = 53;
            protocol = "UDP";
            targetPort = 53;
          }
          {
            name = "dns-tcp";
            port = 53;
            protocol = "TCP";
            targetPort = 53;
          }
          {
            name = "metrics";
            port = 9153;
            protocol = "TCP";
            targetPort = 9153;
          }
        ];
      };
    }
  ];
in
{
  config.chalkos.cluster.kubernetes.addons = lib.mkOrder 400 coredns;
}
