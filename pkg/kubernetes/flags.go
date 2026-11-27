package kubernetes

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// APIServerFlags are the flags chalkos sets on the API server. Extra flags must not override
// them, except overridableAPIServerFlags and enable-admission-plugins, which adds to chalkos's
// plugins.
var APIServerFlags = []string{
	"advertise-address", "allow-privileged", "authentication-config", "authorization-mode", "client-ca-file",
	"enable-admission-plugins", "enable-bootstrap-token-auth", "encryption-provider-config", "etcd-cafile",
	"etcd-certfile", "etcd-keyfile", "etcd-servers", "kubelet-certificate-authority", "kubelet-client-certificate",
	"kubelet-client-key", "kubelet-preferred-address-types", "profiling", "proxy-client-cert-file",
	"proxy-client-key-file", "requestheader-allowed-names", "requestheader-client-ca-file",
	"requestheader-extra-headers-prefix", "requestheader-group-headers", "requestheader-username-headers",
	"secure-port", "service-account-issuer", "service-account-key-file", "service-account-signing-key-file",
	"service-cluster-ip-range", "tls-cert-file", "tls-min-version", "tls-private-key-file",
}

// overridableAPIServerFlags are the flags of APIServerFlags that extra flags may override: they
// tune the API server without changing who may do what or whom it trusts.
var overridableAPIServerFlags = []string{"kubelet-preferred-address-types", "profiling", "tls-min-version"}

// requiredAdmissionPlugins are the admission plugins disable-admission-plugins must not name:
// NodeRestriction keeps kubelets to their own Node and pods, and ValidatingAdmissionPolicy runs
// the revoked nodes' policy.
var requiredAdmissionPlugins = []string{"NodeRestriction", "ValidatingAdmissionPolicy"}

// protectedAPIServerFlag tells whether extra flags must not set the API server's flag name:
// chalkos sets it, or, as with anonymous-auth, leaves it out on purpose.
func protectedAPIServerFlag(name string) bool {
	switch {
	case name == "anonymous-auth":
		return true
	case name == "enable-admission-plugins", slices.Contains(overridableAPIServerFlags, name):
		return false
	}
	return slices.Contains(APIServerFlags, name)
}

// flagConflict is a chalkos flag that the API server refuses to start next to another flag, and
// why chalkos sets it.
type flagConflict struct{ flag, reason string }

var (
	authenticationConflict = flagConflict{"authentication-config", "chalkos configures authentication through " +
		"--authentication-config, and the API server refuses oidc-* flags next to it; chalkos sets no other " +
		"authenticator; grant narrower access with RBAC"}
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
		"server refuses this flag next to it; give further plugins in enable-admission-plugins, which adds them to chalkos's"},
	"service-account-signing-endpoint": {"service-account-signing-key-file", "chalkos signs service account " +
		"tokens with --service-account-signing-key-file and verifies them with --service-account-key-file, " +
		"and the API server refuses this flag next to them"},
}

// protectedKubeletFlags are the kubelet's flags chalkos sets: where it finds its configuration,
// credentials, credential provider, certificates and static pods, the node's name, addresses,
// provider ID, labels and taints, and who may use its API. Extra flags come last on the kubelet's
// command line, so they would override them.
var protectedKubeletFlags = []string{
	"anonymous-auth", "authentication-token-webhook", "authorization-mode", "bootstrap-kubeconfig",
	"cert-dir", "client-ca-file", "config", "config-dir", "hostname-override",
	"image-credential-provider-bin-dir", "image-credential-provider-config", "kubeconfig", "node-ip",
	"node-labels", "pod-manifest-path", "provider-id", "read-only-port", "register-with-taints",
	"rotate-certificates", "tls-cert-file", "tls-private-key-file",
}

// FlagName is a flag's name as the Kubernetes components read it: they take _ for -, so
// anonymous_auth sets --anonymous-auth.
func FlagName(name string) string { return strings.ReplaceAll(name, "_", "-") }

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

// ValidateFlags refuses extra flags of a component, such as kube-apiserver or kubelet, that
// flagNamePattern refuses, that are protected in any spelling, or that conflict with chalkos's;
// errors start with the flag. The API server's disable-admission-plugins must not name
// requiredAdmissionPlugins. Kubelet flags reach it through KUBELET_ARGS, which systemd reads from
// an environment file that drops backslashes, and ExecStart splits at whitespace and then drops
// quotes, so they hold none of those.
func ValidateFlags(component string, flags map[string]string) error {
	protected := map[string]func(string) bool{
		"kube-apiserver": protectedAPIServerFlag,
		"kubelet":        func(name string) bool { return slices.Contains(protectedKubeletFlags, name) },
		// The signers chalkos names; the certificates they issue stay tunable.
		"kube-controller-manager": func(name string) bool {
			return strings.HasPrefix(name, "cluster-signing-") && name != "cluster-signing-duration"
		},
	}
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		if !flagNamePattern.MatchString(name) {
			return fmt.Errorf("flag %q: a flag's name is letters, digits, _, . and -, without the leading dashes", name)
		}
		if p := protected[component]; p != nil && p(FlagName(name)) {
			err := fmt.Errorf("--%s is set by chalkos and cannot be overridden", name)
			if component == "kubelet" && (FlagName(name) == "node-labels" || FlagName(name) == "register-with-taints") {
				err = fmt.Errorf("%w; give the node's labels and taints in the Node document's labels and taints", err)
			}
			if component == "kubelet" && FlagName(name) == "provider-id" {
				err = fmt.Errorf("%w; give the node's provider ID in the Node document's providerID", err)
			}
			return err
		}
		if component != "kube-apiserver" {
			if component == "kubelet" && !KubeletArgument(name+flags[name]) {
				return fmt.Errorf("--%s=%q: the kubelet's flags cannot hold whitespace, quotes, control characters or backslashes", name, flags[name])
			}
			continue
		}
		if conflict, ok := conflictingAPIServerFlags[FlagName(name)]; ok {
			return fmt.Errorf("--%s conflicts with --%s: %s", name, conflict.flag, conflict.reason)
		}
		if FlagName(name) == "disable-admission-plugins" {
			for _, p := range plugins(flags[name]) {
				if slices.Contains(requiredAdmissionPlugins, p) {
					return fmt.Errorf("--%s names %s, which chalkos needs", name, p)
				}
			}
		}
	}
	return nil
}

// KubeletArgument reports whether s can be part of one of the kubelet's arguments, which reach it
// through an environment variable that ExecStart splits at whitespace and then strips quotes
// from, from an environment file that drops backslashes: s holds no whitespace, quote, control
// character or backslash.
func KubeletArgument(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' || r == '\'' || r == '"'
	})
}

// MergeFlags returns a component's flags: base with the extra flags on top. The API server's
// enable-admission-plugins, in any spelling, adds plugins to the base's instead of replacing them.
func MergeFlags(component string, base, extra map[string]string) map[string]string {
	merged := maps.Clone(base)
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		const admission = "enable-admission-plugins"
		if component != "kube-apiserver" || FlagName(name) != admission {
			merged[name] = extra[name]
			continue
		}
		list := plugins(merged[admission])
		for _, p := range plugins(extra[name]) {
			if !slices.Contains(list, p) {
				list = append(list, p)
			}
		}
		merged[admission] = strings.Join(list, ",")
	}
	return merged
}

// plugins returns the admission plugins of a comma-separated list.
func plugins(list string) []string {
	var out []string
	for p := range strings.SplitSeq(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
