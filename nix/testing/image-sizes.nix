# Ceilings for what the test images take, by image, and the installer, in MiB, which the
# image-size check holds them to: the store's data and its hash tree, which upgrades send, and the
# UKI. Each is about 15% above the size measured when it was set; raising one is a change to
# review. The kvm images carry the QEMU guest agent, about 6 MiB of store.
{
  images = {
    k8s-metal = {
      storeData = 281;
      hashTree = 2.2;
      uki = 51;
    };
    # FRR takes about 98 MiB of the Kubernetes image's store, most of it the Python of its closure:
    # frr-reload.py runs on it, and FRR's build configuration references it.
    k8s-kvm = {
      storeData = 389;
      hashTree = 3.1;
      uki = 51;
    };
    # FRR takes about 100 MiB of the router image's store, most of it the Python of its closure:
    # frr-reload.py runs on it, and FRR's build configuration references it.
    router-kvm = {
      storeData = 351;
      hashTree = 2.8;
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
