package kubernetes

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// protectedAPIServerFlags decide who may do what on the API server; chalkos sets them and
// extra flags must not override them.
var protectedAPIServerFlags = []string{"anonymous-auth", "authentication-config", "authorization-mode", "enable-bootstrap-token-auth"}

// flagConflict is a chalkos flag that the API server refuses to start next to another flag, and
// why chalkos sets it.
type flagConflict struct{ flag, reason string }

var (
	authenticationConflict = flagConflict{"authentication-config", "chalkos configures authentication through " +
		"--authentication-config, and the API server refuses oidc-* flags next to it; chalkos offers no way " +
		"to add an authenticator today, so grant narrower access with RBAC"}
	authorizationConflict = flagConflict{"authorization-mode", "chalkos sets --authorization-mode=Node,RBAC, " +
		"and the API server refuses this flag next to it; grant access with RBAC"}
)

// conflictingAPIServerFlags are the API server's flags it refuses to start with next to the flags
// chalkos sets, keyed by name.
var conflictingAPIServerFlags = map[string]flagConflict{
	"oidc-issuer-url": authenticationConflict, "oidc-client-id": authenticationConflict,
	"oidc-ca-file": authenticationConflict, "oidc-username-claim": authenticationConflict,
	"oidc-username-prefix": authenticationConflict, "oidc-groups-claim": authenticationConflict,
	"oidc-groups-prefix": authenticationConflict, "oidc-signing-algs": authenticationConflict,
	"oidc-required-claim":  authenticationConflict,
	"authorization-config": authorizationConflict, "authorization-policy-file": authorizationConflict,
	"authorization-webhook-config-file": authorizationConflict,
	"admission-control": {"enable-admission-plugins", "chalkos sets --enable-admission-plugins, and the API " +
		"server refuses this flag next to it; give the plugins in enable-admission-plugins and disable-admission-plugins"},
	"service-account-signing-endpoint": {"service-account-signing-key-file", "chalkos signs service account " +
		"tokens with --service-account-signing-key-file and verifies them with --service-account-key-file, " +
		"and the API server refuses this flag next to them"},
}

// protectedKubeletFlags are the kubelet's flags chalkos sets: where it finds its configuration,
// credentials, credential provider, certificates and static pods, the node's name, addresses,
// labels and taints, and who may use its API. Extra flags come last on the kubelet's command
// line, so they would override them.
var protectedKubeletFlags = []string{
	"anonymous-auth", "authentication-token-webhook", "authorization-mode", "bootstrap-kubeconfig",
	"cert-dir", "client-ca-file", "config", "config-dir", "hostname-override",
	"image-credential-provider-bin-dir", "image-credential-provider-config", "kubeconfig", "node-ip",
	"node-labels", "pod-manifest-path", "read-only-port", "register-with-taints", "rotate-certificates",
	"tls-cert-file", "tls-private-key-file",
}

// flagName is a flag's name as the Kubernetes components read it: they take _ for -, so
// anonymous_auth sets --anonymous-auth.
func flagName(name string) string { return strings.ReplaceAll(name, "_", "-") }

// flagNamePattern is what a flag's name may be. Flags reach the components as --name=value, so
// a name holding = or whitespace, or starting with -, would set another flag than it names.
var flagNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validateExtraArgs refuses the flags ValidateFlags refuses, naming their component.
func (c Cluster) validateExtraArgs() error {
	for _, component := range slices.Sorted(maps.Keys(c.ExtraArgs)) {
		if err := ValidateFlags(component, c.ExtraArgs[component]); err != nil {
			return fmt.Errorf("extraArgs: %s %w", component, err)
		}
	}
	return nil
}

// ValidateFlags refuses extra flags of a component, such as kube-apiserver or kubelet. Its errors
// start with the flag. It refuses:
//   - names flagNamePattern does not allow;
//   - the component's protected flags, in any spelling it accepts;
//   - the API server's flags that conflict with chalkos's own;
//   - kubelet flags holding whitespace or backslashes.
//
// The kubelet's flags reach it through KUBELET_ARGS. systemd reads that variable from an
// environment file, which drops backslashes, and ExecStart splits the unbraced variable at
// whitespace.
func ValidateFlags(component string, flags map[string]string) error {
	protected := map[string][]string{"kube-apiserver": protectedAPIServerFlags, "kubelet": protectedKubeletFlags}
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		if !flagNamePattern.MatchString(name) {
			return fmt.Errorf("flag %q: a flag's name is letters, digits, _, . and -, without the leading dashes", name)
		}
		if slices.Contains(protected[component], flagName(name)) {
			err := fmt.Errorf("--%s is set by chalkos and cannot be overridden", name)
			if component == "kubelet" && (flagName(name) == "node-labels" || flagName(name) == "register-with-taints") {
				err = fmt.Errorf("%w; give the node's labels and taints in the Node document's labels and taints", err)
			}
			return err
		}
		if conflict, ok := conflictingAPIServerFlags[flagName(name)]; ok && component == "kube-apiserver" {
			return fmt.Errorf("--%s conflicts with --%s: %s", name, conflict.flag, conflict.reason)
		}
	}
	if component != "kubelet" {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		if strings.ContainsAny(name+flags[name], " \t\r\n\\") {
			return fmt.Errorf("--%s=%q: the kubelet's flags cannot hold whitespace or backslashes", name, flags[name])
		}
	}
	return nil
}
