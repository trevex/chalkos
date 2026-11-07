package config

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

// Every image reference with a tag and no digest draws a line naming its document and field; a
// pinned one draws none.
func TestUnpinnedImages(t *testing.T) {
	const digest = "@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d"
	pinned := map[string]string{}
	for field, ref := range testImages {
		if field != "proxy" {
			pinned[field] = ref + digest
		}
	}
	s := kubernetesSet(t, "{images: "+images(t, pinned)+"}")
	got := s.Documents().UnpinnedImages(allSchemas(t))
	want := `Cluster "default": images.proxy: registry.k8s.io/kube-proxy:v1.37.1 names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as registry.k8s.io/kube-proxy:v1.37.1@sha256:<digest>`
	if len(got) != 1 || got[0] != want {
		t.Errorf("UnpinnedImages() = %q, want [%q]", got, want)
	}
	all := kubernetesSet(t, "{}").Documents().UnpinnedImages(allSchemas(t))
	if len(all) != len(testImages) || !strings.Contains(strings.Join(all, "\n"), `Cluster "default": images.flannel: `+testImages["flannel"]+" names a tag") {
		t.Errorf("UnpinnedImages() = %q, want a line for each of the Cluster's images", all)
	}
}

// testImages are the images clusterDoc names, by their fields: those of a flannel image, as the
// test schemas are.
var testImages = map[string]string{
	"apiServer":         "registry.k8s.io/kube-apiserver:v1.37.1",
	"controllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1",
	"scheduler":         "registry.k8s.io/kube-scheduler:v1.37.1",
	"proxy":             "registry.k8s.io/kube-proxy:v1.37.1",
	"etcd":              "registry.k8s.io/etcd:3.7.0-0",
	"coredns":           "registry.k8s.io/coredns/coredns:v1.14.6",
	"pause":             "registry.k8s.io/pause:3.10.2",
	"flannel":           "ghcr.io/flannel-io/flannel:v0.28.9",
}

// kubernetesSet is minimal for a Kubernetes worker with the Cluster spec given.
func kubernetesSet(t *testing.T, spec string) *Set {
	t.Helper()
	s, err := NewSet(mustParse(t, minimal+clusterDoc("default", spec)+nodeDoc("default", "{type: worker}")), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// nodeDoc is a Kubernetes Node document called name with the spec given as YAML.
func nodeDoc(name, spec string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// clusterDoc is a Kubernetes Cluster document called name with the endpoint https://10.0.0.10:6443,
// testImages unless spec names images, and the fields the YAML mapping spec gives.
func clusterDoc(name, spec string) string {
	fields := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(spec), "{"), "}"))
	if !strings.Contains(fields, "images:") {
		b, _ := json.Marshal(testImages)
		fields = strings.TrimSuffix("images: "+string(b)+", "+fields, ", ")
	}
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: " + name + "}\n" +
		`spec: {endpoint: "https://10.0.0.10:6443", ` + fields + "}\n"
}

// images is testImages with the changes given, as a YAML flow mapping; a field given as "" is
// left out.
func images(t *testing.T, changes map[string]string) string {
	t.Helper()
	m := maps.Clone(testImages)
	for k, v := range changes {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
