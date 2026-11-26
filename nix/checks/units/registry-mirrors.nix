# The worker's registry hosts from Mirror and Registry documents: the files containerd reads,
# kept as they were when chalkd fails, and none without documents.
{
  worker,
  unitCheck,
  boundTo,
  ...
}:
let
  # The unit's start, with the capabilities its bounding set leaves.
  script =
    let
      unit = worker.config.systemd.services.chalkos-registry-mirrors.serviceConfig;
    in
    "${boundTo unit.CapabilityBoundingSet} ${unit.ExecStart}";
in
unitCheck "registry-mirrors" [ ] ''
  documents=/run/chalkos/config/oci.chalkos.dev
  certs=/run/containerd/certs.d
  mkdir -p "$certs/stale"
  document oci.chalkos.dev Mirror docker-io '{"registry":"docker.io","endpoints":["https://mirror.example","https://mirror2.example:5000/cache"]}'
  document oci.chalkos.dev Mirror local '{"registry":"[::1]:5000","endpoints":["http://10.0.2.100:5000"]}'
  document oci.chalkos.dev Registry lab '{"host":"10.0.2.100:5000","insecure":true,"username":null,"password":null}'
  ${script}
  registries=$(find "$certs" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
  [ "$registries" = "$(printf '%s\n' 10.0.2.100:5000 docker.io '[::1]:5000' | sort)" ] || fail "the registries are $registries"
  cat >docker.io.toml <<'TOML'
  server = "https://registry-1.docker.io"

  [host."https://mirror.example"]
    capabilities = ["pull", "resolve"]

  [host."https://mirror2.example:5000/cache"]
    capabilities = ["pull", "resolve"]
  TOML
  diff -u docker.io.toml "$certs/docker.io/hosts.toml" || fail "docker.io's hosts.toml"
  # A document chalkd cannot read leaves the mirrors as they were.
  echo '{' >"$documents/Mirror/local.json"
  if ${script}; then fail "an unreadable document was taken"; fi
  diff -u docker.io.toml "$certs/docker.io/hosts.toml" || fail "the mirrors changed"
  # No documents, no mirrors, and no directory left behind.
  rm -r "''${documents:?}"/*
  ${script}
  [ -z "$(ls "$certs")" ] || fail "mirrors without documents: $(ls "$certs")"
  [ "$(ls -A /run/containerd)" = certs.d ] || fail "left behind: $(ls -A /run/containerd)"
''
