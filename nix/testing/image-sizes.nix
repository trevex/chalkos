# Ceilings for what the test images take, by image, and the installer, in MiB, which the
# image-size check holds them to: the store's data and its hash tree, which upgrades send, and the
# UKI. Each is about 15% above the size measured when it was set; raising one is a change to
# review. The kvm images carry the QEMU guest agent, about 6 MiB of store.
{
  images = {
    k8s-metal = {
      storeData = 314;
      hashTree = 2.5;
      uki = 51;
    };
    # FRR takes about 15 MiB of the Kubernetes image's store, which holds no Python.
    # chalkos-registry, the cluster registry, takes about 7.6 MiB of the Kubernetes images' store.
    k8s-kvm = {
      storeData = 330;
      hashTree = 2.6;
      uki = 51;
    };
    router-kvm = {
      storeData = 290;
      hashTree = 2.3;
      uki = 51;
    };
    test-metal = {
      storeData = 222;
      hashTree = 1.8;
      uki = 51;
    };
    test-kvm = {
      storeData = 231;
      hashTree = 1.9;
      uki = 51;
    };
  };
  installer = {
    storeData = 222;
    hashTree = 1.8;
    uki = 51;
  };
}
