package config

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
)

// builtinDoc is a document of a built-in kind with the spec given in YAML flow style.
func builtinDoc(apiVersion, kind, name, spec string) string {
	return "---\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// A host has one Registry and a registry one Mirror, by any of their names; a volume with a disk
// occupies it alone, a selector key left out equal to another left out, and has a mount point of
// its own; and a cluster names each address family once.
func TestBuiltinUnique(t *testing.T) {
	s := schemas(t)
	volume := func(name, spec string) string { return builtinDoc(StorageAPI, "Volume", name, spec) }
	for _, c := range []uniqueCase{
		{"two Registry documents of one registry",
			builtinDoc(OCIAPI, "Registry", "a", `{host: docker.io}`) + builtinDoc(OCIAPI, "Registry", "b", `{host: Index.Docker.io}`),
			`Registry/a, Registry/b: spec.host "docker.io" is not unique; give one Registry per host`},
		{"two Mirror documents of one registry",
			builtinDoc(OCIAPI, "Mirror", "a", `{registry: "ghcr.io", endpoints: ["https://m.example"]}`) +
				builtinDoc(OCIAPI, "Mirror", "b", `{registry: "GHCR.io", endpoints: ["https://n.example"]}`),
			`Mirror/a, Mirror/b: spec.registry "ghcr.io" is not unique; give one Mirror per registry`},
		{"two volumes of one disk",
			volume("a", `{disk: {model: "Samsung*", type: nvme}}`) + volume("b", `{disk: {model: "Samsung*"}}`) +
				volume("c", `{disk: {type: nvme, model: "Samsung*"}}`),
			`Volume/a, Volume/c: (spec.disk.path, spec.disk.model, spec.disk.serial, spec.disk.wwn, spec.disk.size, spec.disk.type) (null, "Samsung*", null, null, null, "nvme") is not unique; a volume with a disk occupies it alone`},
		{"two volumes of one mount point",
			volume("a", `{size: 1G, mountPoint: /srv}`) + volume("b", `{size: 1G, mountPoint: /srv}`) + volume("c", `{size: 1G}`) + volume("d", `{size: 1G}`),
			`Volume/a, Volume/b: spec.mountPoint "/srv" is not unique; mount each volume at a path of its own`},
		{"a family twice",
			builtinDoc("kubernetes.chalkos.dev/v1alpha1", "Cluster", "k", `{endpoint: "https://k.example:6443", ipFamilies: [ipv6, ipv4, ipv6], images: `+kubetest.DocumentImagesWith(t, nil)+`}`),
			`Cluster/k: spec.ipFamilies[0], spec.ipFamilies[2]: "ipv6" is not unique; name each family once`},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs, err := Parse(strings.NewReader(minimal + c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewSet(docs, s); err == nil || err.Error() != c.want {
				t.Errorf("err =\n%v\nwant\n%s", err, c.want)
			}
		})
	}
}
