# Ceilings for what the test cluster's role images take, in MiB, which the image-size check holds
# them to: the store's data and its hash tree, which upgrades send, and the UKI. Each is about 15%
# above the size measured when it was set; raising one is a change to review.
{
  k8s-controlplane = {
    storeData = 281;
    hashTree = 19;
    uki = 51;
  };
  k8s-worker = {
    storeData = 281;
    hashTree = 19;
    uki = 51;
  };
  test = {
    storeData = 222;
    hashTree = 15;
    uki = 51;
  };
}
