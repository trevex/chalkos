# The worker's containerd settings from the Cluster document: containerd's merged config, as
# containerd config dump prints it, takes the sandbox image from the file chalkos-containerd-config
# writes, and keeps its own without the file or without a sandbox image in the document.
{
  worker,
  unitCheck,
  ...
}:
let
  script = worker.config.systemd.services.chalkos-containerd-config.serviceConfig.ExecStart;
  dump = "${worker.pkgs.containerd}/bin/containerd --config ${worker.config.virtualisation.containerd.args.config} config dump";
in
unitCheck "containerd-config" [ ] ''
  sandbox() {
    ${dump} >dump.toml || fail "containerd refused its config"
    sed -n 's/^ *sandbox = //p' dump.toml
  }
  cluster=/run/chalkos/config/kubernetes.chalkos.dev/Cluster/default.json
  pause=registry.example/pause:3.10@sha256:f548e0e8e3dc1896ca956272154dde3314e8cc4fde0a57577ee9fa1c63f5baf4
  own=$(sandbox)
  [ -n "$own" ] || fail "containerd names no sandbox image of its own"
  [ "$own" != "'$pause'" ] || fail "containerd's own sandbox image is the document's"
  mkdir -p "$(dirname "$cluster")"
  printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Cluster","metadata":{"name":"default"},"spec":{"images":{"pause":"%s"}}}\n' "$pause" >"$cluster"
  ${script}
  [ "$(sandbox)" = "'$pause'" ] || fail "the sandbox image is $(sandbox), not the document's"
  printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Cluster","metadata":{"name":"default"},"spec":{}}\n' >"$cluster"
  ${script}
  [ "$(sandbox)" = "$own" ] || fail "without a sandbox image in the document it is $(sandbox)"
''
