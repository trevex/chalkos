# The Kubernetes releases an image's kubelet comes from, by minor: Kubernetes's support window of
# the three newest minors. nixpkgs's own release is the package of the binary cache; the two minors
# before it are the newest patch release of each, whose kubelet the builder compiles from source.
# `nix build .#kubelet-releases` builds every entry, which a change of the table needs once.
{ lib, pkgs }:
let
  # The kubelet alone of a release nixpkgs does not package. The package's build also makes the
  # generated docs, the man pages, the pause binary and kubeadm's completions, and patches the
  # addon manager; none of them concerns the kubelet, and older trees need not fit them.
  kubeletRelease =
    version: hash:
    (pkgs.kubernetes.override { components = [ "cmd/kubelet" ]; }).overrideAttrs {
      inherit version;
      src = pkgs.fetchFromGitHub {
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
        make "SHELL=${pkgs.runtimeShell}" "WHAT=$WHAT"
        runHook postBuild
      '';
      installPhase = ''
        runHook preInstall
        install -D _output/local/go/bin/kubelet -t $out/bin
        runHook postInstall
      '';
    };
in
{
  ${lib.versions.majorMinor pkgs.kubernetes.version} = pkgs.kubernetes;
  "1.36" = kubeletRelease "1.36.6" "sha256-4/231gwj+g9A1oDQKIdmJ6lrfquVhTLSxe6hzFuaDFA=";
  "1.35" = kubeletRelease "1.35.10" "sha256-LaqG0zNn43JEt04isr8MTtLdH+M59EZcWpC0o64U2FY=";
}
