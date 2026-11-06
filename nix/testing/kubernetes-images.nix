# Container images the Kubernetes e2e test serves from its registry, fetched by the digest of their
# amd64 manifest, so the test runs without network access. Each is an OCI layout that keeps the
# manifest as the registry served it, so the test's registry serves it under the same digest and
# the nodes pull the cluster's pinned references through the Mirror. images.json maps each
# reference, as the e2e clusters name it, to its layout: <name>:<tag>@<digest>, and the workload's
# busybox, which the test pulls by its tag, <name>:<tag>.
{ pkgs }:
let
  images = [
    {
      name = "registry.k8s.io/kube-apiserver";
      tag = "v1.37.1";
      digest = "sha256:1f207ab66b1ca6579dd5bf1024396015ca2a0f638157a7741d2d88d7b3c82c51";
      hash = "sha256-ipcbwAdQETHK9TRZyYcsQDLO+k/mc0LmUkCU2f7pNVE=";
    }
    {
      name = "registry.k8s.io/kube-controller-manager";
      tag = "v1.37.1";
      digest = "sha256:24366808a9bf9dfefb3aa171f0fadac61a1dde50a43e578c42eb3d8785cb606e";
      hash = "sha256-fp1tWLMGhv1b6xy0AKPJAe0+d4M5HDA36yiIFQ7EEBg=";
    }
    {
      name = "registry.k8s.io/kube-scheduler";
      tag = "v1.37.1";
      digest = "sha256:e4fc6f552722518aaa7d6cddbb8b638df275a5adbc5d1e5d59276af52692fb2c";
      hash = "sha256-ugoOn2ufyQDj2o/450VCTkUidfsqRWFcoW/qNNnGgbA=";
    }
    {
      name = "registry.k8s.io/kube-proxy";
      tag = "v1.37.1";
      digest = "sha256:abbadc84931b520750bcda3350819f6a828ee44a4a0cab548a26016aa7e2a3b7";
      hash = "sha256-gv3WvhqAZ0ooTzz6DWqpLtKwzMhXZFyVnZ+5Ib42dDg=";
    }
    {
      name = "registry.k8s.io/etcd";
      tag = "3.7.0-0";
      digest = "sha256:1f445fa5dd06b08f3c0cf9f46e2891e9bc66cbf7e0b9c66627c40658edf4bc98";
      hash = "sha256-CEHvcKeufeG1rUC/s9kMUkB+fos+EsQiAEtXNDtSPj4=";
    }
    {
      name = "registry.k8s.io/pause";
      tag = "3.10.2";
      digest = "sha256:412c4a7219cb8a299a37337f3d87810c5340095322e15594a1637785adad0f17";
      hash = "sha256-fjGj0MQL79KNuyjo5ZdJD7vf/B2ZNDXfwKl//Cb1AQg=";
    }
    {
      name = "registry.k8s.io/coredns/coredns";
      tag = "v1.14.6";
      digest = "sha256:1ba6f47265602e2e50a9c4669e3a955e4298a0d30dc82f39293d4bf1a851e0ff";
      hash = "sha256-1Cobn9wIwXNcSt+excK9XonRuanhKTtM2WafqPy3vQc=";
    }
    {
      name = "ghcr.io/flannel-io/flannel";
      tag = "v0.28.9";
      digest = "sha256:d666a036197fe6c8928f82b670d89f38f5830d25e70bfcc35641da06f03cfcad";
      hash = "sha256-tDJIRBrWIgndC89owvMQYRXd18PUlllTJKxEeZe1Gro=";
    }
    # The test's workload, which it pulls by its tag.
    {
      name = "docker.io/library/busybox";
      tag = "1.37.0";
      digest = "sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4";
      hash = "sha256-Cizv1sUJyz4wxAYoRKkSGEGSINnFkdWP+Ts97hxlQsw=";
      pinned = false;
    }
  ];
  # The image as an OCI layout of the amd64 manifest alone, copied with the manifest's bytes kept.
  # skopeo's index.json differs between its releases, so the layout's index is written again with
  # the manifest's descriptor alone, and the hash covers the image's own bytes.
  layout =
    image:
    pkgs.runCommand "${baseNameOf image.name}-${image.tag}-oci"
      {
        nativeBuildInputs = [
          pkgs.skopeo
          pkgs.jq
        ];
        outputHashMode = "recursive";
        outputHashAlgo = "sha256";
        outputHash = image.hash;
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
        impureEnvVars = pkgs.lib.fetchers.proxyImpureEnvVars;
      }
      ''
        export HOME=$TMPDIR
        skopeo --insecure-policy --override-os linux --override-arch amd64 copy --preserve-digests \
          docker://${image.name}@${image.digest} oci:$out
        jq -c '{schemaVersion: 2, manifests: [.manifests[] | {mediaType, digest, size}]}' \
          $out/index.json >index.json
        mv index.json $out/index.json
      '';
  # The Kubernetes components the e2e clusters name, whose tags follow the kubelet of the test
  # images: nixpkgs's Kubernetes release.
  stale = map (image: "${image.name}:${image.tag}") (
    builtins.filter (
      image:
      pkgs.lib.hasPrefix "registry.k8s.io/kube-" image.name && image.tag != "v${pkgs.kubernetes.version}"
    ) images
  );
in
assert pkgs.lib.assertMsg (stale == [ ])
  "nix/testing/kubernetes-images.nix pins ${toString stale}, but the test images' kubelet is ${pkgs.kubernetes.version}";
pkgs.writeTextDir "images.json" (
  builtins.toJSON (
    builtins.listToAttrs (
      map (image: {
        name =
          "${image.name}:${image.tag}" + pkgs.lib.optionalString (image.pinned or true) "@${image.digest}";
        value = layout image;
      }) images
    )
  )
)
