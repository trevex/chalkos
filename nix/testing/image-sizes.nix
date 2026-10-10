# Ceilings for what the test cluster's role images take on each platform, and its installer, in
# MiB, which the image-size check holds them to: the store's data and its hash tree, which
# upgrades send, and the UKI. Each is about 15% above the size measured when it was set; raising
# one is a change to review. The kvm images carry the QEMU guest agent, about 6 MiB of store.
{
  roles = {
    k8s-controlplane = {
      metal = {
        storeData = 281;
        hashTree = 2.2;
        uki = 51;
      };
      kvm = {
        storeData = 290;
        hashTree = 2.3;
        uki = 51;
      };
    };
    k8s-worker = {
      metal = {
        storeData = 281;
        hashTree = 2.2;
        uki = 51;
      };
      kvm = {
        storeData = 290;
        hashTree = 2.3;
        uki = 51;
      };
    };
    test = {
      metal = {
        storeData = 222;
        hashTree = 1.8;
        uki = 51;
      };
      kvm = {
        storeData = 231;
        hashTree = 1.9;
        uki = 51;
      };
    };
  };
  installer = {
    storeData = 222;
    hashTree = 1.8;
    uki = 51;
  };
}
