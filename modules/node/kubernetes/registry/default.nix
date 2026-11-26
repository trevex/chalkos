# The cluster registry on images with Kubernetes whose chalkos.kubernetes.registry.enable is true:
# the ClusterRegistry kind, one per cluster, whose endpoints name the nodes that serve it. On those
# nodes alone the output cluster-registry renders the port, and chalkos-registry serves on it as a
# user of its own, behind a firewall table that opens the port. The unit is ready once the port
# listens, so the effects after its restart, the kubelet's among them, find it answering. Its
# port serves a client with a certificate of the OS CA alone, a node's or a client file's; its
# socket serves reads to its group, as the diagnostic cluster-registry lists its repositories.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (chalkosLib.internal)
    builtin
    cel
    ;
  inherit (chalkosLib.sandbox) hardened;
  inherit (chalkosLib.internal.api) groups apiVersions;
  inherit (chalkosLib.types) address;
  k = config.chalkos.kubernetes;
  # The Node documents of the delivered config, by name.
  nodes = cel.documents "config" apiVersions.kubernetes "Node";

  render = lib.getExe pkgs.chalkos.chalkos-render;
  registry = lib.getExe pkgs.chalkos.chalkos-registry;
  nft = lib.getExe pkgs.nftables;
  dir = config.chalkos.outputs.cluster-registry.dir;
  firewall = config.networking.firewall.enable;
  # The packet mark bit of the cluster registry's port, which the unit's table sets; beside the
  # control plane's 0x02000000 and VXLAN's 0x01000000 of firewall.nix. kube-proxy and flannel
  # leave it alone, and every packet the node receives has it cleared, so a network plugin of
  # the cluster's own (cni.provider "none") must not use it.
  mark = "0x04000000";
  # chalkd's node certificate and the OS CAs, which root alone may read; the unit gets copies as
  # its credentials node.pem and ca.crt.
  credentials = {
    "node.pem" = "/state/chalkd/node.pem";
    "ca.crt" = "/state/chalkd/ca.crt";
  };
  storage = "/var/lib/chalkos-registry";
  # The socket that serves reads without a certificate, to the registry's group alone.
  socket = "/run/chalkos-registry/registry.sock";
  # The specs of the Volume documents of the config c mounted at the storage or above it, in the
  # order of their names.
  storageMounts =
    c:
    ''${
      cel.specs c apiVersions.storage "Volume"
    }.filter(s, s.mountPoint != null && (s.mountPoint == "${storage}" || "${storage}".startsWith(s.mountPoint + "/")))'';
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "registry" {
    carries = "chalkos-registry, the cluster registry,";
    kinds = [ "ClusterRegistry" ];
  };

  config = lib.mkIf (k.enable && k.registry.enable) {
    chalkos.tests = import ./tests.nix { inherit pkgs; };
    chalkos.kinds.ClusterRegistry = builtin groups.kubernetes // {
      description = "The cluster registry: chalkos-registry on the nodes whose static addresses its endpoints name, every control plane among them. containerd on every node pulls the images of the mirrored hosts from it first, before their Mirror endpoints and the hosts themselves.";
      spec = {
        mirrors = chalkosLib.mkOption {
          # A host is the first component of the repository paths below which the cluster registry
          # holds its images, as the distribution spec takes it in any case (Go lower-cases it):
          # neither a port nor an IPv6 address, and no two separators in a row.
          type = types.nonEmptyListOf (types.strMatching "[A-Za-z0-9]+(([._]|__|-+)[A-Za-z0-9]+)*");
          unique = {
            across = "list";
            as = "host";
            hint = "name each host once";
          };
          example = [
            "docker.io"
            "ghcr.io"
            "registry.k8s.io"
          ];
          description = "Registry hosts whose images containerd pulls from the cluster registry first, as image references name them, without a port; docker.io is Docker Hub. The cluster registry holds an image below its host's name, in lower case: docker.io/library/nginx is <endpoint>/docker.io/library/nginx.";
        };
        endpoints = chalkosLib.mkOption {
          type = types.nonEmptyListOf address;
          unique = {
            across = "list";
            as = "ip";
            hint = "name each address once";
          };
          example = [
            "fdc8:a1b2:c3d4:ff::11"
            "fdc8:a1b2:c3d4:ff::12"
          ];
          description = "IP addresses of the nodes that serve the cluster registry, in the order nodes pull from them; a node that serves it pulls from itself first. A node serves it when one of these is one of its static addresses, the Address= values of its Network documents, and every control plane serves it. chalkctl registry push pushes to each, and nothing copies images between them.";
          # node.addresses are the node's static addresses, as its node certificate names them;
          # ip() compares every spelling of an address alike.
          rules = [
            {
              description = "Every control plane serves the cluster registry: one of its static addresses is an endpoint.";
              rule = ''cel.bind(nodes, ${nodes}, !nodes.exists(n, nodes[n].spec.type == "controlplane")) || node.addresses.exists(a, self.exists(e, ip(e) == ip(a)))'';
              messageExpression = ''"the control plane " + node.name + " serves the cluster registry, but the ClusterRegistry's endpoints name none of its static addresses (" + (size(node.addresses) == 0 ? "none" : node.addresses.join(", ")) + "); add one of them to the endpoints and apply the config to every node"'';
            }
          ];
        };
        port = mkOption {
          type = types.ints.between 1024 65535;
          default = 5000;
          description = "TCP port the cluster registry serves on, over HTTPS with the node certificate: 1024 or above, as chalkos-registry runs without privileges.";
        };
      };
    };

    # The unit's port and firewall table on the nodes that serve the cluster registry, nothing
    # elsewhere.
    chalkos.outputs.cluster-registry = {
      inputs = [
        "ClusterRegistry"
        "Network"
      ];
      render = "${render} cluster-registry ${mark}";
      # nft checks a table with the kernel, which takes CAP_NET_ADMIN; the check has it in a user
      # and network namespace of its own.
      # A node that serves no registry gets no table.
      check = "${render} check each registry.nft ${pkgs.util-linux}/bin/unshare --map-root-user --net ${nft} --check --file $FILE";
    };
    # A node that starts or stops serving starts or stops the registry. A Volume mounted at or
    # above the storage starts it again too: systemd hands the new file system to the registry's
    # user, and the registry stores on it, only as the unit starts. The unit is Type=notify, so
    # the restart returns once the registry listens, and the effects after this one, such as the
    # kubelet's, find it answering.
    chalkos.effects.cluster-registry = {
      outputs = [ "cluster-registry" ];
      when = "${storageMounts "config"} != ${storageMounts "oldConfig"}";
      restart = [ "chalkos-registry.service" ];
      start = true;
    };

    # Pinned IDs, as the router's users have them: an image that adds or drops the feature moves
    # no other user's ID, nor the owner of the registry's files. 333 is free in NixOS's ids.nix.
    users.users.chalkos-registry = {
      isSystemUser = true;
      uid = 333;
      group = "chalkos-registry";
      description = "The cluster registry";
    };
    users.groups.chalkos-registry.gid = 333;

    # The registry serves on the nodes with a rendered port alone, as a user of its own without
    # capabilities. It reads the node certificate and the OS CAs once, from its credentials, and
    # keeps its images in its state directory. Its runtime directory holds the socket of reads,
    # which its group may reach. A Volume may be mounted there: the unit starts
    # after the mount, and systemd gives the directory, and everything below it when its owner
    # differs, to the unit's user before it starts, so a fresh file system is the registry's too.
    # Nothing it stores runs as a program, even where an exec path or a Volume would allow it.
    systemd.services.chalkos-registry = {
      description = "Serve the cluster registry";
      wantedBy = [ "multi-user.target" ];
      after = [
        "chalkos-config.service"
        "network.target"
      ]
      ++ lib.optional firewall "nftables.service";
      # containerd's first pulls of the control plane's images find it serving.
      before = [ "kubelet.service" ];
      unitConfig = {
        ConditionPathExists = "${dir}/registry.env";
        RequiresMountsFor = [
          storage
          "/state"
        ];
      };
      serviceConfig =
        hardened
        // {
          # chalkos-registry tells systemd once its port is open.
          Type = "notify";
          User = "chalkos-registry";
          Group = "chalkos-registry";
          EnvironmentFile = "${dir}/registry.env";
          ExecStart = "${registry} serve --port \${PORT} --socket ${socket} --storage ${storage}";
          LoadCredential = lib.mapAttrsToList (name: file: "${name}:${file}") credentials;
          StateDirectory = "chalkos-registry";
          StateDirectoryMode = "0700";
          RuntimeDirectory = "chalkos-registry";
          RuntimeDirectoryMode = "0750";
          Restart = "on-failure";
          RestartSec = 5;
          RestrictAddressFamilies = [
            "AF_INET"
            "AF_INET6"
            "AF_UNIX"
          ];
        }
        # The table goes with the registry, so a node that stops serving closes the port; +
        # runs the commands as root with every capability.
        // lib.optionalAttrs firewall {
          ExecStartPre = "+${nft} -f ${dir}/registry.nft";
          ExecStopPost = "-+${nft} delete table inet chalkos-registry";
        };
    };

    # chalkd replaces the node certificate when it renews it, and the OS CAs when they rotate,
    # each by a rename; the registry, which read them once, starts again with the new ones. A
    # node that serves no registry runs none, which try-restart leaves stopped.
    systemd.paths.chalkos-registry-credentials-changed = {
      wantedBy = [ "multi-user.target" ];
      pathConfig = {
        PathChanged = lib.attrValues credentials;
        Unit = "chalkos-registry-credentials-changed.service";
      };
    };
    systemd.services.chalkos-registry-credentials-changed = {
      description = "Start the cluster registry again with chalkd's new credentials";
      # --no-block queues the restart and ends the service at once: a path unit does not start
      # an active service again, so waiting for the registry's restart would miss a change of
      # the other file during it.
      serviceConfig = chalkosLib.sandbox.noExec // {
        Type = "oneshot";
        ExecStart = "${config.systemd.package}/bin/systemctl --no-block try-restart chalkos-registry.service";
      };
    };

    # What the registry's table marks, TCP to its port alone; see firewall.nix for the other
    # marks.
    networking.firewall.extraInputRules = lib.mkIf firewall "meta mark & ${mark} == ${mark} accept";

    chalkos.diagnostics.cluster-registry = {
      description = "Lists the repositories of the cluster registry the node serves, as its catalog does, or says why it does not answer.";
      command = [
        registry
        "catalog"
        socket
      ];
      role = "reader";
      timeout = "10s";
      groups = [ "chalkos-registry" ];
    };
  };
}
