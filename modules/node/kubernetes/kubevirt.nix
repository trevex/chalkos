# KubeVirt: the kubevirt module group, loaded on demand. kubevirt.iommu turns the IOMMU on for
# devices passed through to virtual machines and loads vfio_pci, which binds them: no pod can load
# it, as containers hold no CAP_SYS_MODULE.
{
  chalkosLib,
  config,
  lib,
  ...
}:
let
  k = config.chalkos.kubernetes;
in
{
  options.chalkos.kubernetes =
    lib.recursiveUpdate
      (chalkosLib.internal.feature "Kubernetes" "kubevirt" {
        carries = "the kernel modules of KubeVirt's virtual machines, KVM, vhost, VFIO and UIO, loaded on demand";
      })
      {
        kubevirt.iommu = lib.mkOption {
          type = lib.types.bool;
          default = false;
          description = ''
            Whether the kernel turns the IOMMU on in passthrough mode and loads `vfio_pci` at
            boot, as KubeVirt needs to pass PCI devices through to virtual machines. AMD's IOMMU
            is on unless the kernel's command line turns it off; this adds
            `intel_iommu=on iommu=pt`. Only with `chalkos.kubernetes.kubevirt.enable`.
          '';
        };
      };

  config = lib.mkIf (k.enable && k.kubevirt.enable) {
    chalkos.kernel.moduleGroups = [ "kubevirt" ];
    boot.kernelParams = lib.mkIf k.kubevirt.iommu [
      "intel_iommu=on"
      "iommu=pt"
    ];
    boot.kernelModules = lib.mkIf k.kubevirt.iommu [ "vfio_pci" ];
  };
}
