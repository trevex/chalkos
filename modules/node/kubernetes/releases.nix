# The Kubernetes releases an image's kubelet comes from, by minor: Kubernetes's support window of
# the three newest minors, each pinned to a patch release and the hash of its source, from which
# kubelet.nix builds the kubelet. publish marks the minors whose images a release of chalkos
# builds. nixpkgs's own release has no part in it, so a change of nixpkgs leaves the kubelet as it
# is. After a change of a version, `nix run .#update-pins` sets the hashes and the pins that follow
# the newest release, and `nix build .#kubelet-releases` builds every kubelet. The newest minor
# comes first: Renovate's configuration takes the first for the newest, and the renovate check
# holds it to that.
{
  "1.37" = {
    version = "1.37.2";
    hash = "sha256-Bbyu5hMQTSrvr/kDFHlRnfz5RIR7ecBfTFOGTCffn5c=";
    publish = true;
  };
  "1.36" = {
    version = "1.36.6";
    hash = "sha256-4/231gwj+g9A1oDQKIdmJ6lrfquVhTLSxe6hzFuaDFA=";
  };
  "1.35" = {
    version = "1.35.10";
    hash = "sha256-LaqG0zNn43JEt04isr8MTtLdH+M59EZcWpC0o64U2FY=";
  };
}
