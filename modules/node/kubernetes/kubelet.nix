# The kubelet alone of a Kubernetes release of releases.nix, built from source. nixpkgs's
# kubernetes package serves as the build recipe, with the release's version and source. Its build
# also makes the generated docs, the man pages, the pause binary and kubeadm's completions, and
# patches the addon manager; none of them concerns the kubelet, and other trees need not fit them.
# The kubelet must report the release's version, which its build reads from the source.
{
  fetchFromGitHub,
  hash,
  kubernetes,
  runtimeShell,
  version,
}:
(kubernetes.override { components = [ "cmd/kubelet" ]; }).overrideAttrs {
  inherit version;
  src = fetchFromGitHub {
    owner = "kubernetes";
    repo = "kubernetes";
    tag = "v${version}";
    inherit hash;
  };
  outputs = [ "out" ];
  patches = [ ];
  buildPhase = ''
    runHook preBuild
    patchShebangs ./hack
    make "SHELL=${runtimeShell}" "WHAT=$WHAT"
    runHook postBuild
  '';
  installPhase = ''
    runHook preInstall
    install -D _output/local/go/bin/kubelet -t $out/bin
    runHook postInstall
  '';
  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    reported=$($out/bin/kubelet --version)
    if [ "$reported" != "Kubernetes v${version}" ]; then
      echo "error: the kubelet of Kubernetes ${version} reports $reported" >&2
      exit 1
    fi
    runHook postInstallCheck
  '';
}
