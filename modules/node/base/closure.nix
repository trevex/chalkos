# What the store leaves out. The UKI on the ESP carries the kernel and the initrd, so the system
# does not link them as well: nothing on a node boots from the store's copies.
{
  config,
  lib,
  options,
  pkgs,
  ...
}:
{
  # NixOS links both into the system's store path.
  system.systemBuilderCommands = lib.mkAfter ''
    rm $out/kernel $out/initrd
  '';
  # nixos-init reads the system's bootspec in the initrd, so it stays, without the initrd and with
  # the UKI's path on the ESP as the kernel, which a bootspec must name.
  boot.bootspec.writer = lib.mkForce ''
    ${options.boot.bootspec.writer.default}
    ${lib.getExe pkgs.buildPackages.jq} --sort-keys --arg kernel /efi${config.image.repart.verityStore.ukiPath} \
      '."org.nixos.bootspec.v1" |= (del(.initrd) | .kernel = $kernel)' $out/boot.json >boot.json
    mv boot.json $out/boot.json
  '';
  # The build fails should anything else bring them back. The paths only name what to look for,
  # so the system's build does not depend on them.
  system.forbiddenDependenciesRegexes =
    map (path: "^${lib.escapeRegex (builtins.unsafeDiscardStringContext "${path}")}$")
      [
        config.boot.kernelPackages.kernel
        config.system.build.initialRamdisk
      ];
}
