// Package layertest checks a layer's API against the permissions its plugin gives, for the tests
// of chalkd and of its plugins.
package layertest

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/pki"
)

// CheckPermissions fails t unless perms has an entry for every method of the service and no
// other: chalkd refuses a procedure without one.
func CheckPermissions(t *testing.T, service protoreflect.ServiceDescriptor, perms map[string]layer.Permission) {
	t.Helper()
	methods := service.Methods()
	if methods.Len() == 0 {
		t.Fatalf("%s has no methods", service.FullName())
	}
	for i := range methods.Len() {
		procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(i).Name())
		if _, ok := perms[procedure]; !ok {
			t.Errorf("%s has no permission", procedure)
		}
	}
	if len(perms) != methods.Len() {
		t.Errorf("%d permissions for the %d methods of %s", len(perms), methods.Len(), service.FullName())
	}
}

// CheckComments fails t unless the comment of each RPC of the proto file, which the API
// reference shows, names the modes, role and layer its permission gives; layerName is "" for the
// node API.
func CheckComments(t *testing.T, protoFile string, service protoreflect.ServiceDescriptor, perms map[string]layer.Permission, layerName string) {
	t.Helper()
	data, err := os.ReadFile(protoFile)
	if err != nil {
		t.Fatal(err)
	}
	available := regexp.MustCompile(`Available in (maintenance and normal|maintenance|normal) mode to (readers|operators|admins|nodes)(?:, on images with the (\w+) layer)?\.`)
	rpc := regexp.MustCompile(`^rpc (\w+)\(`)
	modes := map[string][]nodev1.Mode{"maintenance and normal": layer.AnyMode, "maintenance": layer.MaintenanceOnly, "normal": layer.NormalOnly}
	roles := map[string]pki.Role{"readers": pki.RoleReader, "operators": pki.RoleOperator, "admins": pki.RoleAdmin, "nodes": pki.RoleNode}
	var comment []string
	rpcs := 0
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if text, ok := strings.CutPrefix(line, "//"); ok {
			comment = append(comment, strings.TrimSpace(text))
			continue
		}
		if m := rpc.FindStringSubmatch(line); m != nil {
			rpcs++
			p := perms["/"+string(service.FullName())+"/"+m[1]]
			a := available.FindStringSubmatch(strings.Join(comment, " "))
			switch {
			case a == nil:
				t.Errorf("the comment of %s names no modes and role", m[1])
			case !slices.Equal(modes[a[1]], p.Modes) || roles[a[2]] != p.Role || strings.ToLower(a[3]) != layerName:
				t.Errorf("the comment of %s says %q, the permission is %v to %s of the layer %q", m[1], a[0], p.Modes, p.Role, layerName)
			}
		}
		comment = nil
	}
	if rpcs != len(perms) {
		t.Errorf("%s declares %d RPCs, the permissions have %d", protoFile, rpcs, len(perms))
	}
}
