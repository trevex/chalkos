package kubernetes

import (
	"strings"
	"testing"
)

const imageJSON = `{"version": "1.37.1", "cni": "flannel"}`

func TestReadImage(t *testing.T) {
	img, err := ReadImage(writeFile(t, imageJSON))
	if err != nil {
		t.Fatal(err)
	}
	if img.Version != "1.37.1" || img.CNI != CNIFlannel {
		t.Errorf("image = %+v", img)
	}
}

func TestParseImageRejects(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"no version":  {`"version": "1.37.1"`, `"version": ""`, "no Kubernetes version"},
		"bad version": {`"version": "1.37.1"`, `"version": "1.37"`, `the kubelet's version: "1.37" is no version`},
		"unknown CNI": {`"cni": "flannel"`, `"cni": "calico"`, `unknown CNI provider "calico"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseImage([]byte(strings.Replace(imageJSON, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
