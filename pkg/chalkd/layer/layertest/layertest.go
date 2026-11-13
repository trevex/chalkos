// Package layertest checks a layer's API against the permissions its plugin gives, and the
// arguments its commands refuse, for the tests of chalkd and of its plugins.
package layertest

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// CheckConsole fails t when perms let the console call a procedure that needs the admin or the
// node role, other than as one of its actions, or when the console's actions are other than the
// procedures given: whoever sits at the machine reads the node and acts on it as an operator,
// and rolls its config back.
func CheckConsole(t *testing.T, perms map[string]layer.Permission, actions ...string) {
	t.Helper()
	var got []string
	for procedure, p := range perms {
		switch {
		case p.Console == layer.ConsoleAction:
			got = append(got, procedure)
		case p.Console != layer.ConsoleDenied && (p.Role == pki.RoleAdmin || p.Role == pki.RoleNode):
			t.Errorf("the console may call %s, which needs the %s role", procedure, p.Role)
		}
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(actions)); !slices.Equal(got, want) {
		t.Errorf("the console's actions are %v, want %v", got, want)
	}
}

// CheckComments fails t unless the comment of each RPC of the proto file, which the API
// reference shows, names the modes, role and layer its permission gives, and whether the console
// may call it: "The console may call it." or "The console may call it while the image allows the
// console's actions."; layerName is "" for the node API.
func CheckComments(t *testing.T, protoFile string, service protoreflect.ServiceDescriptor, perms map[string]layer.Permission, layerName string) {
	t.Helper()
	data, err := os.ReadFile(protoFile)
	if err != nil {
		t.Fatal(err)
	}
	available := regexp.MustCompile(`Available in (maintenance and normal|maintenance|normal) mode to (readers|operators|admins|nodes)(?:, on images with the (\w+) layer)?\.`)
	console := regexp.MustCompile(`The console may call it( while the image allows the console's actions)?\.`)
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
			joined := strings.Join(comment, " ")
			a := available.FindStringSubmatch(joined)
			switch {
			case a == nil:
				t.Errorf("the comment of %s names no modes and role", m[1])
			case !slices.Equal(modes[a[1]], p.Modes) || roles[a[2]] != p.Role || strings.ToLower(a[3]) != layerName:
				t.Errorf("the comment of %s says %q, the permission is %v to %s of the layer %q", m[1], a[0], p.Modes, p.Role, layerName)
			}
			said := layer.ConsoleDenied
			if c := console.FindStringSubmatch(joined); c != nil {
				said = layer.ConsoleAllowed
				if c[1] != "" {
					said = layer.ConsoleAction
				}
			}
			if said != p.Console {
				t.Errorf("the comment of %s gives the console %v, the permission %v", m[1], said, p.Console)
			}
		}
		comment = nil
	}
	if rpcs != len(perms) {
		t.Errorf("%s declares %d RPCs, the permissions have %d", protoFile, rpcs, len(perms))
	}
}

// Refused reports whether cmd refuses args before it runs, which chalkd reports as a usage error:
// a flag it does not take, a required flag missing, or positional arguments its Args refuses.
// cmd's flags keep what args set.
func Refused(cmd *cobra.Command, args []string) bool {
	if err := cmd.ParseFlags(args); err != nil {
		return true
	}
	return cmd.ValidateRequiredFlags() != nil || cmd.ValidateFlagGroups() != nil || cmd.ValidateArgs(cmd.Flags().Args()) != nil
}
