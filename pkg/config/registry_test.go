package config

import (
	"reflect"
	"strings"
	"testing"
)

const registryDocs = `apiVersion: chalkos.dev/v1alpha1
kind: Registry
metadata: {name: ghcr}
spec: {host: ghcr.io, username: ci-bot, password: {secretRef: {name: ghcr, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lan}
spec: {host: "10.0.0.5:5000"}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: ghcr}
spec: {data: {token: s3cret}}
`

// TestRegistryCredentialsOf gives each Registry's host with its user and the password its
// secretRef names, and none for a registry pulled from anonymously; it refuses two documents of
// one host and a password the config does not hold.
func TestRegistryCredentialsOf(t *testing.T) {
	docs, err := Parse(strings.NewReader(registryDocs))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RegistryCredentialsOf(docs)
	want := []RegistryCredentials{{Host: "ghcr.io", Username: "ci-bot", Password: "s3cret"}, {Host: "10.0.0.5:5000"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("RegistryCredentialsOf = %+v, %v; want %+v", got, err, want)
	}
	for _, tc := range []struct{ name, docs, want string }{
		{"two of one host", registryDocs + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: again}\nspec: {host: ghcr.io}\n",
			`Registry "ghcr" and Registry "again" both name the host ghcr.io; give one Registry document per host`},
		{"a missing key", strings.Replace(registryDocs, "key: token", "key: nope", 1),
			`Registry "ghcr" names key "nope" of Secret "ghcr", which the config does not hold`},
	} {
		docs, err := Parse(strings.NewReader(tc.docs))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RegistryCredentialsOf(docs); err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
