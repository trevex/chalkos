# Container images the Kubernetes e2e test serves from its registry, fetched by the digest of their
# amd64 manifest, so the test runs without network access. Each is an OCI layout that keeps the
# manifest as the registry served it, so the test's registry serves it under the same digest and
# the nodes pull the cluster's pinned references through the Mirror. images.json maps each
# reference, as the e2e clusters name it, to its layout: <name>:<tag>@<digest>, and the workload's
# busybox, which the test pulls by its tag (pinned false), <name>:<tag>.
#
# kubernetes-images.json lists them, which `nix run .#update-pins` keeps up with
# modules/node/kubernetes/releases.nix. The images marked haStart are the previous patch release
# of the control plane's, which the HA test's cluster config names before the test rolls its
# control plane out through config to the newest.
{ pkgs, kubernetesReleases }:
let
  inherit (pkgs) lib;
  images = lib.importJSON ./kubernetes-images.json;
  imageLayout = import ./image-layout.nix { inherit pkgs; };
  layout =
    image:
    pkgs.runCommand "${baseNameOf image.name}-${image.tag}-oci"
      {
        nativeBuildInputs = [ imageLayout ];
        outputHashMode = "recursive";
        outputHashAlgo = "sha256";
        outputHash = image.hash;
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
        impureEnvVars = lib.fetchers.proxyImpureEnvVars;
      }
      ''
        export HOME=$TMPDIR
        image-layout ${image.name}@${image.digest} $out
      '';
  # The Kubernetes components the e2e clusters name follow the kubelet of the test images: the
  # newest release chalkos pins. Those the HA test starts from are an earlier patch release of the
  # same minor, which the skew rules let the API server run behind the kubelet by.
  kubeletMinor = lib.last (lib.sort lib.versionOlder (lib.attrNames kubernetesReleases));
  kubelet = kubernetesReleases.${kubeletMinor}.version;
  stale = map (image: "${image.name}:${image.tag}") (
    builtins.filter (
      image:
      lib.hasPrefix "registry.k8s.io/kube-" image.name
      && (
        if image.haStart or false then
          lib.versions.majorMinor (lib.removePrefix "v" image.tag) != kubeletMinor
          || builtins.compareVersions (lib.removePrefix "v" image.tag) kubelet >= 0
        else
          image.tag != "v${kubelet}"
      )
    ) images
  );
in
assert lib.assertMsg (stale == [ ])
  "nix/testing/kubernetes-images.json pins ${toString stale}, but the test images' kubelet is ${kubelet}: the cluster's components follow it, and those the HA test starts from are an earlier patch release of ${kubeletMinor}; run nix run .#update-pins";
pkgs.writeTextDir "images.json" (
  builtins.toJSON (
    builtins.listToAttrs (
      map (image: {
        name = "${image.name}:${image.tag}" + lib.optionalString (image.pinned or true) "@${image.digest}";
        value = layout image;
      }) images
    )
  )
)
