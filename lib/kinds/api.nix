# The API groups of chalkos's own kinds, their version and apiVersions, and networkd's kinds.
# Rules name kinds by apiVersion from here rather than from config.chalkos.kinds.<Kind>, since
# some kinds, such as Flannel, exist only on some images.
rec {
  version = "v1alpha1";
  groups = {
    core = "chalkos.dev";
    oci = "oci.chalkos.dev";
    storage = "storage.chalkos.dev";
    time = "time.chalkos.dev";
    kubernetes = "kubernetes.chalkos.dev";
    networkd = "networkd.chalkos.dev";
  };
  apiVersions = builtins.mapAttrs (_: group: "${group}/${version}") groups;
  # networkd's kinds, whose schemas come from the tables of the image's systemd.
  networkdKinds = [
    "Link"
    "NetDev"
    "Network"
  ];
}
