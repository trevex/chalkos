# The end-to-end tests, which boot the test images in QEMU: each check runs the tests the pattern
# matches, with the image directories the variables named give and the further variables given.
{ pkgs, checkLib, ... }:
let
  inherit (pkgs) lib;
  inherit (checkLib) chalkPkgs testEnv;
  e2e =
    name: pattern: imageDirs: vars:
    pkgs.runCommand "chalkos-e2e-${name}"
      (
        {
          requiredSystemFeatures = [ "kvm" ];
          nativeBuildInputs = testEnv.tools ++ [ chalkPkgs.chalklab-e2e ];
        }
        // testEnv.e2eEnv imageDirs vars
      )
      ''
        export HOME=$TMPDIR
        ${checkLib.runTests "chalklab-e2e -test.v -test.run '${pattern}' -test.timeout 60m"}
        touch $out
      '';
  kubernetesImages = "${chalkPkgs.test-kubernetes-images}/images.json";
  # A disk that a previous system left with an LVM volume group on its one partition, as the
  # installer's target: the group's volume stays inactive, so the install can wipe the disk. The
  # volume is created without activating it, which needs no device mapper in the build's VM.
  lvmDisk = pkgs.vmTools.runInLinuxVM (
    pkgs.runCommand "chalkos-test-lvm-disk"
      {
        nativeBuildInputs = [
          pkgs.lvm2
          pkgs.util-linux
        ];
        preVM = ''
          mkdir $out
          diskImage=$out/disk.raw
          ${pkgs.qemu-utils}/bin/qemu-img create -f raw $diskImage 64M
        '';
      }
      ''
        echo 'label: gpt' | sfdisk /dev/vda
        echo ',,L' | sfdisk --append /dev/vda
        lvm=(--config 'devices { use_devicesfile = 0 } activation { udev_sync = 0 udev_rules = 0 }')
        pvcreate "''${lvm[@]}" /dev/vda1
        vgcreate "''${lvm[@]}" pool /dev/vda1
        lvcreate "''${lvm[@]}" --activate n --zero n -n data -l 100%FREE pool
      ''
  );
in
{
  e2e-firmware = e2e "firmware" "^TestFirmwareBoots$" [ ] { };
  e2e-image = e2e "image" "^TestImageBootsWithoutSecureBoot$" [ "CHALKLAB_IMAGE_DIR" ] { };
  e2e-secureboot = e2e "secureboot" "^TestSecureBoot" [ "CHALKLAB_IMAGE_DIR" ] { };
  e2e-verity = e2e "verity" "^TestVerityRejectsTamperedStore$" [ "CHALKLAB_IMAGE_DIR" ] { };
  e2e-install = e2e "install" "^TestInstallInPlace$" [ "CHALKLAB_IMAGE_DIR" ] { };
  e2e-installer = e2e "installer" "^TestInstallerInstallsOntoLVMDisk$" [
    "CHALKLAB_IMAGE_DIR"
    "CHALKLAB_INSTALLER_DIR"
  ] { CHALKLAB_LVM_DISK = "${lvmDisk}/disk.raw"; };
  e2e-iso = e2e "iso" "^TestInstallerISOBoots$" [ "CHALKLAB_GENERIC_INSTALLER_DIR" ] { };
  e2e-storage = e2e "storage" "^TestStorage" [ "CHALKLAB_STORAGE_IMAGE_DIR" ] { };
  e2e-upgrade = e2e "upgrade" "^TestUpgrade$" [
    "CHALKLAB_IMAGE_DIR"
    "CHALKLAB_UPGRADE_IMAGE_DIR"
    "CHALKLAB_UNHEALTHY_IMAGE_DIR"
  ] { };
  e2e-kubernetes = e2e "kubernetes" "^TestKubernetesCluster$" [ "CHALKLAB_K8S_IMAGE_DIR" ] {
    CHALKLAB_CHALKLAB = lib.getExe chalkPkgs.chalklab;
    CHALKLAB_K8S_IMAGES = kubernetesImages;
  };
  e2e-router = e2e "router" "^TestRouterLab$" [ "CHALKLAB_ROUTER_IMAGE_DIR" ] {
    CHALKLAB_CHALKLAB = lib.getExe chalkPkgs.chalklab;
  };
  e2e-kubernetes-ha =
    e2e "kubernetes-ha" "^TestKubernetesHA$"
      [
        "CHALKLAB_K8S_HA_IMAGE_DIR"
        "CHALKLAB_K8S_HA_UPGRADE_IMAGE_DIR"
      ]
      {
        CHALKLAB_K8S_IMAGES = kubernetesImages;
      };
}
