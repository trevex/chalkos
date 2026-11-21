# A kernel module from outside the image, built for the kernel of the Kubernetes test image,
# which e2e-kubernetes hands to a worker through a pod: the module policy must refuse it.
{ pkgs, kernel }:
pkgs.stdenv.mkDerivation {
  pname = "chalkos-e2e-outside-module";
  version = kernel.modDirVersion;
  src = ../../test/e2e/modules/outside;
  nativeBuildInputs = kernel.moduleBuildDependencies;
  buildPhase = ''
    runHook preBuild
    make -C ${kernel.dev}/lib/modules/${kernel.modDirVersion}/build M=$PWD \
      ${toString kernel.commonMakeFlags} modules
    runHook postBuild
  '';
  installPhase = ''
    runHook preInstall
    install -Dm444 chalkos_e2e_outside.ko $out/chalkos_e2e_outside.ko
    runHook postInstall
  '';
}
