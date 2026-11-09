# The worker's registry hosts from Mirror and Registry documents: the files containerd reads,
# kept as they were when chalkd fails, and none without documents.
{
  worker,
  unitCheck,
  ...
}:
let
  script = worker.config.systemd.services.chalkos-registry-mirrors.serviceConfig.ExecStart;
in
unitCheck "registry-mirrors" [ ] ''
  documents=/run/chalkos/config/oci.chalkos.dev
  certs=/run/containerd/certs.d
  mkdir -p "$documents/Mirror" "$documents/Registry" "$certs/stale"
  cat >"$documents/Mirror/docker-io.json" <<'JSON'
  {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Mirror","metadata":{"name":"docker-io"},
   "spec":{"registry":"docker.io","endpoints":["https://mirror.example","https://mirror2.example:5000/cache"]}}
  JSON
  cat >"$documents/Mirror/local.json" <<'JSON'
  {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Mirror","metadata":{"name":"local"},
   "spec":{"registry":"[::1]:5000","endpoints":["http://10.0.2.100:5000"]}}
  JSON
  cat >"$documents/Registry/lab.json" <<'JSON'
  {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Registry","metadata":{"name":"lab"},
   "spec":{"host":"10.0.2.100:5000","insecure":true,"username":null,"password":null}}
  JSON
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
  cat >local.toml <<'TOML'
  server = "https://[::1]:5000"

  [host."http://10.0.2.100:5000"]
    capabilities = ["pull", "resolve"]
  TOML
  diff -u local.toml "$certs/[::1]:5000/hosts.toml" || fail "[::1]:5000's hosts.toml"
  cat >10.0.2.100:5000.toml <<'TOML'
  server = "http://10.0.2.100:5000"
  TOML
  diff -u 10.0.2.100:5000.toml "$certs/10.0.2.100:5000/hosts.toml" || fail "10.0.2.100:5000's hosts.toml"
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
