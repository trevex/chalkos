# The image chalklab e2e tests boot: the base modules plus the probe, with small partitions.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  imports = [ ./probe.nix ];

  chalkos.disk = {
    espSize = "260M";
    storeSize = "2G";
    storeVeritySize = "64M";
    stateSize = "64M";
  };

  # chalklab attaches disks as virtio-blk-pci, which the initrd needs to find the store. The kvm
  # platform loads them itself, and the Kubernetes tests boot its images to prove it. The metal
  # platform leaves them out, as real machines have no virtio disks, so its test images, which run
  # under QEMU too, get them here.
  boot.initrd.availableKernelModules = lib.mkMerge [
    (lib.mkIf (config.chalkos.platform.name != "kvm") [
      "virtio_pci"
      "virtio_blk"
    ])
    # systemd imports the credentials of SMBIOS, chalktest-ask's below, once, in the initrd,
    # where it loads dmi_sysfs to read them.
    [ "dmi_sysfs" ]
  ];

  # The probe writes its facts to the first serial port itself, whichever console is last, and
  # chalkos-serial-log@ copies chalkd's lines there.
  boot.kernelParams = [ "quiet" ];

  # The e2e tests read the screen with this font, which they find at CHALKLAB_CONSOLE_FONT. The
  # initrd loads it, so the passphrase prompts show in it too.
  console = {
    earlySetup = true;
    font = "${pkgs.kbd}/share/consolefonts/default8x16.psfu.gz";
  };

  # A password request of the booted system, as a unit that needs a password after the boot
  # makes. Only a boot given the credential chalktest.ask makes it; the console on tty1 answers
  # it, and the unit logs the answer.
  systemd.services.chalktest-ask = {
    description = "Ask for a word as a unit that needs a password does";
    wantedBy = [ "multi-user.target" ];
    after = [ "chalkos-console.service" ];
    unitConfig.ConditionCredential = "chalktest.ask";
    path = [ config.systemd.package ];
    script = ''
      echo "answer $(systemd-ask-password --no-tty --timeout=0 --id=chalktest 'chalktest asks for a word')"
    '';
    serviceConfig.Type = "exec";
  };

  # Certificates last a year; a test renews them by delivering config, due or not.
  systemd.services.chalkd.environment.CHALKD_TEST_RENEW_ON_APPLY_CONFIG = "1";
}
