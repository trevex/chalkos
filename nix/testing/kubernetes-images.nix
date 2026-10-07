# Container images the Kubernetes e2e test serves from its registry, fetched by digest (amd64),
# so the test runs without network access. images.json maps each reference, as the cluster's
# nodes pull it, to its docker archive.
{ pkgs, kubernetesVersion }:
let
  images = [
    {
      name = "registry.k8s.io/kube-apiserver";
      tag = "v1.37.1";
      digest = "sha256:1f207ab66b1ca6579dd5bf1024396015ca2a0f638157a7741d2d88d7b3c82c51";
      hash = "sha256-tzJA1MLwCqPrF32pAmBSB5IeF/EPNa2eHeiahAUi/Bg=";
    }
    {
      name = "registry.k8s.io/kube-controller-manager";
      tag = "v1.37.1";
      digest = "sha256:24366808a9bf9dfefb3aa171f0fadac61a1dde50a43e578c42eb3d8785cb606e";
      hash = "sha256-u3hHE0lS7IsHR8z/RkWaJirBFlGP5ZbycpVX+YV9fs8=";
    }
    {
      name = "registry.k8s.io/kube-scheduler";
      tag = "v1.37.1";
      digest = "sha256:e4fc6f552722518aaa7d6cddbb8b638df275a5adbc5d1e5d59276af52692fb2c";
      hash = "sha256-FDQHWEJGHlk1YFRfnTKFyFQ7xZri5Q4mlqYqQjeJGOw=";
    }
    {
      name = "registry.k8s.io/kube-proxy";
      tag = "v1.37.1";
      digest = "sha256:abbadc84931b520750bcda3350819f6a828ee44a4a0cab548a26016aa7e2a3b7";
      hash = "sha256-L4wbY0C1/K4Zpm2HDC60w1o3GtTfDDXAtSrbwqTbVuM=";
    }
    {
      name = "registry.k8s.io/etcd";
      tag = "3.7.0-0";
      digest = "sha256:1f445fa5dd06b08f3c0cf9f46e2891e9bc66cbf7e0b9c66627c40658edf4bc98";
      hash = "sha256-/6xqVktC/lxw113eZgmVIdbChWWLAuEsnUp5uLwhQgk=";
    }
    {
      name = "registry.k8s.io/pause";
      tag = "3.10.2";
      digest = "sha256:412c4a7219cb8a299a37337f3d87810c5340095322e15594a1637785adad0f17";
      hash = "sha256-cUFZbZrduX9FQBtrZnceMqXtK0qgRwFqySZT2YlPOJs=";
    }
    {
      name = "registry.k8s.io/coredns/coredns";
      tag = "v1.14.6";
      digest = "sha256:1ba6f47265602e2e50a9c4669e3a955e4298a0d30dc82f39293d4bf1a851e0ff";
      hash = "sha256-FBp3wbQ9tkw9nf5w1tgs6ysx8UBBHaHxUHL7TJYK9L0=";
    }
    {
      name = "ghcr.io/flannel-io/flannel";
      tag = "v0.28.9";
      digest = "sha256:d666a036197fe6c8928f82b670d89f38f5830d25e70bfcc35641da06f03cfcad";
      hash = "sha256-sYSH1GsSHSlCpbmT2/0/fti1F5jCb9rU4FbehSDvtYQ=";
    }
    # The test's workload.
    {
      name = "docker.io/library/busybox";
      tag = "1.37.0";
      digest = "sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4";
      hash = "sha256-98/1k//BY97IullBA0SWUTAsMhNoD264phJa5wqiBzE=";
    }
  ];
  archive =
    image:
    pkgs.dockerTools.pullImage {
      imageName = image.name;
      imageDigest = image.digest;
      finalImageTag = image.tag;
      inherit (image) hash;
    };
in
# The control plane runs the images of its kubelet's release.
assert pkgs.lib.assertMsg (kubernetesVersion == "1.37.1")
  "nixpkgs moved Kubernetes to ${kubernetesVersion}; pin that release's images in nix/testing/kubernetes-images.nix";
pkgs.writeTextDir "images.json" (
  builtins.toJSON (
    builtins.listToAttrs (
      map (image: {
        name = "${image.name}:${image.tag}";
        value = archive image;
      }) images
    )
  )
)
