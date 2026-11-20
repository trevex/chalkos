# The images a release of chalkos publishes, by the name of the repository each is pushed to below
# ghcr.io/trevex/chalkos: kubernetes-<minor>-<platform> for each Kubernetes minor
# modules/node/kubernetes/releases.nix marks publish, and router-<platform>, on metal and KVM.
# Each is mkImage's image, with its repository and tag besides: the tag is v<version> of
# version.nix, which the images record as their tag, with the commit's revision. They carry no OS
# CA, so in maintenance mode they accept any client until they are installed, and are unsigned:
# whoever enrols Secure Boot keys signs them. Their manifests name chalkos's source, its licence
# Apache-2.0 and, as the time they were created, the commit's. A Kubernetes image is named
# kubernetes-<platform> whatever its minor, so chalkctl upgrade --image finds the nodes of the
# previous minor by its name.
{ self, lib }:
let
  inherit (self.lib) version;
  tag = "v${version}";
  registry = "ghcr.io/trevex/chalkos";
  platforms = [
    "metal"
    "kvm"
  ];
  # The commit's time in RFC 3339, from lastModifiedDate, which is YYYYMMDDHHMMSS in UTC.
  created =
    let
      d = builtins.substring;
      date = self.lastModifiedDate;
    in
    "${d 0 4 date}-${d 4 2 date}-${d 6 2 date}T${d 8 2 date}:${d 10 2 date}:${d 12 2 date}Z";
  annotations = {
    "org.opencontainers.image.source" = "https://github.com/trevex/chalkos";
    "org.opencontainers.image.licenses" = "Apache-2.0";
  }
  // lib.optionalAttrs (self ? lastModifiedDate) {
    "org.opencontainers.image.created" = created;
  };
  # Each kind of image: the start of its repository's and of its image's name, and its modules.
  kinds =
    lib.mapAttrsToList (minor: _: {
      repository = "kubernetes-${minor}";
      name = "kubernetes";
      modules = [
        self.nixosModules.kubernetes
        { chalkos.kubernetes.version = minor; }
      ];
    }) (lib.filterAttrs (_: release: release.publish) self.lib.kubernetesReleases)
    ++ [
      {
        repository = "router";
        name = "router";
        modules = [ self.nixosModules.router ];
      }
    ];
  imagesOf =
    kind:
    let
      images = self.lib.mkImages {
        osCA = null;
        inherit tag;
        revision = self.rev or self.dirtyRev or null;
        modules = [ { chalkos.imageDirectory.annotations = annotations; } ];
        images = map (platform: {
          name = "${kind.name}-${platform}";
          inherit platform;
          inherit (kind) modules;
        }) platforms;
      };
    in
    map (
      platform:
      let
        name = "${kind.repository}-${platform}";
      in
      lib.nameValuePair name (
        images."${kind.name}-${platform}"
        // {
          repository = "${registry}/${name}";
          inherit tag;
        }
      )
    ) platforms;
in
lib.listToAttrs (lib.concatMap imagesOf kinds)
