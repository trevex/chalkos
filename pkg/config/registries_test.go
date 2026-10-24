package config

import (
	"reflect"
	"strings"
	"testing"
)

const registriesDocs = `apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: hub}
spec: {host: docker.io, username: ci-bot, password: {secretRef: {name: hub, key: token}}}
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lan}
spec: {host: "10.0.0.5:5000", insecure: true}
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: hub}
spec: {registry: index.docker.io, endpoints: ["https://mirror.example/cache", "http://10.0.0.5:5000"]}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: hub}
spec: {data: {token: s3cret}}
`

func parseRegistries(t *testing.T, text string) Documents {
	t.Helper()
	docs, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// TestRegistriesOf gives each Registry's host, user, password and insecure flag, and each
// Mirror's endpoints, finds them by any name of their host, and leaves passwords empty without
// Secrets to take them from.
func TestRegistriesOf(t *testing.T) {
	docs := parseRegistries(t, registriesDocs)
	got, err := RegistriesOf(docs, docs.Secrets().Lookup)
	want := Registries{
		Logins:  []RegistryCredentials{{Host: "docker.io", Username: "ci-bot", Password: "s3cret"}, {Host: "10.0.0.5:5000", Insecure: true}},
		Mirrors: []RegistryMirror{{Registry: "index.docker.io", Endpoints: []string{"https://mirror.example/cache", "http://10.0.0.5:5000"}}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("RegistriesOf = %+v, %v; want %+v", got, err, want)
	}
	if l, ok := got.Login("INDEX.docker.io"); !ok || l.Username != "ci-bot" {
		t.Errorf("Login(INDEX.docker.io) = %+v, %v", l, ok)
	}
	if _, ok := got.Login("ghcr.io"); ok {
		t.Error("Login(ghcr.io) found a Registry")
	}
	if e := got.Endpoints("docker.io"); !reflect.DeepEqual(e, want.Mirrors[0].Endpoints) {
		t.Errorf("Endpoints(docker.io) = %q", e)
	}
	if e := got.Endpoints("ghcr.io"); e != nil {
		t.Errorf("Endpoints(ghcr.io) = %q, want none", e)
	}
	bare, err := RegistriesOf(docs, nil)
	if err != nil || bare.Logins[0].Password != "" || bare.Logins[0].Username != "ci-bot" {
		t.Errorf("without Secrets: %+v, %v", bare, err)
	}
}

// TestRegistriesOfRefuses refuses two Registry or two Mirror documents of one registry, by any of
// its names, an http:// endpoint whose host has no Registry that allows plain HTTP, and a
// password the config does not hold.
func TestRegistriesOfRefuses(t *testing.T) {
	const again = "---\napiVersion: oci.chalkos.dev/v1alpha1\nkind: Mirror\nmetadata: {name: again}\nspec: {registry: %s, endpoints: [\"https://m.example\"]}\n"
	for _, tc := range []struct{ name, docs, want string }{
		{"two Registry documents of one host", registriesDocs + "---\napiVersion: oci.chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: again}\nspec: {host: docker.io}\n",
			`Registry "hub" and Registry "again" both name the host docker.io; give one Registry document per host`},
		{"two Registry documents of one registry", registriesDocs + "---\napiVersion: oci.chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: again}\nspec: {host: index.docker.io}\n",
			`Registry "hub" and Registry "again" name the same registry index.docker.io, as docker.io and index.docker.io; give one Registry document per host`},
		{"two Mirror documents of one registry", registriesDocs + strings.Replace(again, "%s", "index.docker.io", 1),
			`Mirror "hub" and Mirror "again" both mirror index.docker.io; give one Mirror document per registry`},
		{"two Mirror documents by two names", registriesDocs + strings.Replace(again, "%s", "Docker.io", 1),
			`Mirror "hub" and Mirror "again" mirror the same registry index.docker.io, as index.docker.io and Docker.io; give one Mirror document per registry`},
		{"a plain HTTP endpoint without insecure", strings.Replace(registriesDocs, ", insecure: true", "", 1),
			`Mirror "hub": the endpoint http://10.0.0.5:5000 is plain HTTP, which nothing authenticates; give 10.0.0.5:5000 a Registry with insecure: true to allow it`},
		{"a plain HTTP endpoint without a Registry", strings.Replace(registriesDocs, `"10.0.0.5:5000", insecure`, `"10.0.0.6:5000", insecure`, 1),
			`Mirror "hub": the endpoint http://10.0.0.5:5000 is plain HTTP, which nothing authenticates; give 10.0.0.5:5000 a Registry with insecure: true to allow it`},
		{"an endpoint with a user", strings.Replace(registriesDocs, "https://mirror.example/cache", "https://bot:s3cret@mirror.example/cache", 1),
			`Mirror "hub": the endpoint "https://bot:xxxxx@mirror.example/cache" holds a user; give the credentials of mirror.example in its Registry instead`},
		{"a missing key", strings.Replace(registriesDocs, "key: token", "key: nope", 1),
			`Registry "hub" names key "nope" of Secret "hub", which the config does not hold`},
	} {
		docs := parseRegistries(t, tc.docs)
		if _, err := RegistriesOf(docs, docs.Secrets().Lookup); err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// TestHostsTOML writes a hosts.toml for each mirrored registry, below docker.io for Docker Hub,
// with the endpoints in order and then the registry over HTTPS, and one for each host whose
// Registry allows plain HTTP, reached over HTTP.
func TestHostsTOML(t *testing.T) {
	r, err := RegistriesOf(parseRegistries(t, registriesDocs), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"docker.io": `server = "https://registry-1.docker.io"

[host."https://mirror.example/cache"]
  capabilities = ["pull", "resolve"]

[host."http://10.0.0.5:5000"]
  capabilities = ["pull", "resolve"]
`,
		"10.0.0.5:5000": "server = \"http://10.0.0.5:5000\"\n",
	}
	if got := r.HostsTOML(); !reflect.DeepEqual(got, want) {
		t.Errorf("HostsTOML = %q, want %q", got, want)
	}
}
