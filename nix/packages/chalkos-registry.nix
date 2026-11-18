# chalkos-registry, the OCI registry of the Kubernetes layer's cluster registry: distribution's
# registry handlers with the filesystem storage driver alone, behind chalkos's TLS and
# authorisation, stripped.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-registry";
  paths = [
    ../../cmd/chalkos-registry
    ../../pkg/registry
    # The roles of client certificates, and the OS CAs' bundle.
    ../../pkg/pki
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
