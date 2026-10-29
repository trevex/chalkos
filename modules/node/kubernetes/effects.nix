# What a change of the node's config restarts or reloads on an image that runs Kubernetes: the
# registry units, the control plane's ports, the preparation and the kubelet, and chalkd's
# Kubernetes loops, through the Kubernetes layer's actions.
{ config, lib, ... }:
let
  cel = import ../../../lib/cel.nix { inherit lib; };
  inherit (import ../../../lib/kinds/api.nix) apiVersions;
  # Where chalkd writes the node's Kubernetes files, as default.nix names it.
  run = "/run/chalkos/kubernetes";
  # The Address= values of each Network's [Network] sections, as written and in the order of the
  # networks' names, which the identity takes the node's static addresses from. They are compared
  # with their prefix lengths: taking those off loops over the addresses of each section of each
  # network, which costs more than a rule may.
  staticAddresses =
    c:
    "cel.bind(d, ${
      cel.documents c apiVersions.networkd "Network"
    }, d.map(n, has(d[n].spec.network) ? ${cel.oneOrMany "d[n].spec.network"}.map(s, type(s) == map && has(s.address) ? s.address : null) : []))";
  # The cluster the Cluster and Flannel documents give, the endpoint among it, or a cluster file
  # that changed, as one a boot whose config loader failed left missing.
  clusterChanged = lib.concatStringsSep " || " [
    "facts.cluster != oldFacts.cluster"
    (cel.specsChanged apiVersions.kubernetes "Cluster")
    (cel.specsChanged apiVersions.kubernetes "Flannel")
  ];
  # A share that differs in more than its node CA: the node CA is read from the share whenever a
  # node certificate is issued, and nothing else uses it, so a new one restarts nothing. The share
  # fact, declared in kinds/builtin/kubernetes.nix, keeps the hashes of its keys.
  withoutNodeCA = s: ''${s}.filter(k, k != "nodeCA").map(k, k + "=" + ${s}[k])'';
  shareChanged = "cel.bind(was, oldFacts.share, cel.bind(gets, facts.share, was == null || gets == null ? was != gets : ${withoutNodeCA "was"} != ${withoutNodeCA "gets"}))";
  # A change of what the preparation and the kubelet read: the preparation writes the node's
  # address, the firewall's VXLAN rule, the kubelet's credentials and flags and the control
  # plane's certificates from the share, the node's identity and its cluster. Other parts of the
  # identity, such as the storage or the time servers, restart neither.
  kubernetesChanged = lib.concatStringsSep " || " [
    # The node's type, which is the cluster's kind, its labels, taints, addresses and its
    # kubelet's extra flags.
    (cel.specsChanged apiVersions.kubernetes "Node")
    # The Host's host name, which the identity's Kubernetes node carries beside its name.
    (cel.specsChanged apiVersions.core "Host")
    "${staticAddresses "config"} != ${staticAddresses "oldConfig"}"
    clusterChanged
    shareChanged
  ];
  # A control plane applies the objects of changed Addon documents at once, as its loop does
  # whenever it starts. A worker applies none: chalkctl delivers none to it, and one it is given
  # anyway changes nothing it runs.
  addonObjects = c: "${cel.specs c apiVersions.kubernetes "Addon"}.map(s, s.objects).flatten()";
  addonsApplied = ''(cel.bind(d, ${
    cel.documents "config" apiVersions.kubernetes "Node"
  }, d.exists(n, d[n].spec.type == "controlplane")) && ${addonObjects "config"} != ${addonObjects "oldConfig"})'';
in
{
  config = lib.mkIf config.chalkos.kubernetes.enable {
    chalkos.effects = {
      # The registry mirrors read the Mirror and Registry documents when they start; no
      # password reaches hosts.toml, so a changed Secret alone restarts nothing.
      registry-mirrors = {
        when = "${cel.specsChanged apiVersions.oci "Mirror"} || ${cel.specsChanged apiVersions.oci "Registry"}";
        restart = [ "chalkos-registry-mirrors.service" ];
      };
      # The kubelet reads its credential provider's config when it starts and caches answers, so
      # a Registry change, or a change of a password it references, writes the files again and
      # starts the kubelet again, which empties the cache; its pods keep running.
      registry-credentials = {
        kinds = [ "Registry" ];
        restart = [
          "chalkos-registry-credentials.service"
          "kubelet.service"
        ];
      };
      # The control plane's ports read the Node document when they start; it references no
      # Secret.
      controlplane-ports = {
        when = cel.specsChanged apiVersions.kubernetes "Node";
        restart = [ "chalkos-controlplane.service" ];
      };
      # Both start too: a node runs no kubelet until it holds a share, which the delivery of
      # its first one changes.
      kubernetes = {
        when = kubernetesChanged;
        restart = [
          "chalkos-kubernetes.service"
          "kubelet.service"
        ];
        start = true;
        # A unit that fails to restart says why the preparation failed, which tells more than
        # systemctl's error.
        hint = {
          text = "the node runs no kubelet: preparation failed";
          file = "${run}/prepare.error";
        };
      };
      # chalkd's Kubernetes loops, which chalkd starts again itself: every loop reads the
      # cluster when it starts, and the control plane's loop reads the share and applies the
      # control plane's objects.
      chalkd-cluster = {
        when = clusterChanged;
        chalkd = "kubernetes:restart";
      };
      # A restart for a new cluster starts the control plane's loop again too, so a reload is due
      # only without one.
      chalkd-share = {
        when = "(${shareChanged} || ${addonsApplied}) && !(${clusterChanged})";
        chalkd = "kubernetes:reload";
      };
    };
  };
}
