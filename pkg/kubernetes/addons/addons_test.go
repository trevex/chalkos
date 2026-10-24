package addons

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

func testCluster(families ...nodeip.Family) kubernetes.Cluster {
	return kubernetes.Cluster{
		Kind: kubernetes.KindControlPlane, Endpoint: "https://10.0.0.10:6443", Version: "1.37.1", Domain: "lab.local",
		IPFamilies:   families,
		PodCIDRs:     kubernetes.ByFamily[netip.Prefix]{IPv4: netip.MustParsePrefix("10.244.0.0/16"), IPv6: netip.MustParsePrefix("fd00:10:244::/56")},
		ServiceCIDRs: kubernetes.ByFamily[netip.Prefix]{IPv4: netip.MustParsePrefix("10.96.0.0/12"), IPv6: netip.MustParsePrefix("fd00:10:96::/112")},
		DNSIPs:       kubernetes.ByFamily[netip.Addr]{IPv4: netip.MustParseAddr("10.96.0.10"), IPv6: netip.MustParseAddr("fd00:10:96::a")},
		Images:       kubernetes.Images{KubeProxy: "proxy", CoreDNS: "coredns", Flannel: "flannel"},
		Flannel:      &kubernetes.Flannel{MTU: 1430},
	}
}

// find returns the object of the kind and name, or fails the test.
func find(t *testing.T, objects []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, o := range objects {
		if o["kind"] == kind && o["metadata"].(map[string]any)["name"] == name {
			return o
		}
	}
	t.Fatalf("no %s %s", kind, name)
	return nil
}

func data(t *testing.T, o map[string]any, key string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(o["data"].(map[string]any)[key].(string)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderOrder(t *testing.T) {
	objects, err := Render(testCluster("ipv4"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objects {
		names = append(names, o["kind"].(string)+"/"+o["metadata"].(map[string]any)["name"].(string))
	}
	// RBAC, kube-proxy, flannel, CoreDNS.
	want := "ClusterRoleBinding/chalkos:cluster-admins ClusterRoleBinding/chalkos:selfnodeclient ClusterRoleBinding/chalkos:apiserver-kubelet " +
		"ServiceAccount/kube-proxy ClusterRoleBinding/chalkos:node-proxier ConfigMap/kube-proxy DaemonSet/kube-proxy " +
		"Namespace/kube-flannel ClusterRole/flannel ClusterRoleBinding/flannel ServiceAccount/flannel ConfigMap/kube-flannel-cfg DaemonSet/kube-flannel-ds " +
		"ServiceAccount/coredns ClusterRole/system:coredns ClusterRoleBinding/system:coredns ConfigMap/coredns Deployment/coredns Service/kube-dns"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("objects =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderFollowsTheCluster(t *testing.T) {
	objects, err := Render(testCluster("ipv6", "ipv4"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := data(t, find(t, objects, "ConfigMap", "kube-proxy"), "config.conf")
	if proxy["clusterCIDR"] != "fd00:10:244::/56,10.244.0.0/16" || proxy["bindAddress"] != "::" {
		t.Errorf("kube-proxy = %v", proxy)
	}
	net := data(t, find(t, objects, "ConfigMap", "kube-flannel-cfg"), "net-conf.json")
	if net["Network"] != "10.244.0.0/16" || net["IPv6Network"] != "fd00:10:244::/56" || net["Backend"].(map[string]any)["MTU"] != 1430.0 {
		t.Errorf("flannel = %v", net)
	}
	dns := find(t, objects, "Service", "kube-dns")["spec"].(map[string]any)
	if dns["clusterIP"] != "fd00:10:96::a" || dns["ipFamilies"].([]any)[0] != "IPv6" {
		t.Errorf("kube-dns = %v", dns)
	}
	corefile := find(t, objects, "ConfigMap", "coredns")["data"].(map[string]any)["Corefile"].(string)
	if !strings.Contains(corefile, "kubernetes lab.local in-addr.arpa ip6.arpa {") {
		t.Errorf("Corefile = %s", corefile)
	}
}

func TestRenderWithoutFlannel(t *testing.T) {
	c := testCluster("ipv4")
	c.Flannel = nil
	objects, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o["metadata"].(map[string]any)["namespace"] == "kube-flannel" || o["kind"] == "Namespace" {
			t.Errorf("rendered %v without flannel", o["metadata"])
		}
	}
}

// asJSON is o as JSON decodes it, so tests read nested fields with one set of types.
func asJSON(t *testing.T, o map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRenderFollowsTheFamilies: kube-proxy serves every family and binds by the primary one,
// flannel's network has a range of each family, and the cluster DNS has an address of the
// primary family.
func TestRenderFollowsTheFamilies(t *testing.T) {
	backend := map[string]any{"Type": "vxlan"}
	for name, tc := range map[string]struct {
		families          []nodeip.Family
		clusterCIDR, bind string
		netConf           map[string]any
		dnsIP, dnsFamily  string
	}{
		"ipv4": {[]nodeip.Family{"ipv4"}, "10.244.0.0/16", "0.0.0.0",
			map[string]any{"Network": "10.244.0.0/16", "EnableNFTables": true, "Backend": backend}, "10.96.0.10", "IPv4"},
		"dual": {[]nodeip.Family{"ipv4", "ipv6"}, "10.244.0.0/16,fd00:10:244::/56", "0.0.0.0",
			map[string]any{"Network": "10.244.0.0/16", "EnableIPv6": true, "IPv6Network": "fd00:10:244::/56", "EnableNFTables": true, "Backend": backend},
			"10.96.0.10", "IPv4"},
		"ipv6 primary": {[]nodeip.Family{"ipv6", "ipv4"}, "fd00:10:244::/56,10.244.0.0/16", "::",
			map[string]any{"Network": "10.244.0.0/16", "EnableIPv6": true, "IPv6Network": "fd00:10:244::/56", "EnableNFTables": true, "Backend": backend},
			"fd00:10:96::a", "IPv6"},
		"ipv6": {[]nodeip.Family{"ipv6"}, "fd00:10:244::/56", "::",
			map[string]any{"EnableIPv4": false, "EnableIPv6": true, "IPv6Network": "fd00:10:244::/56", "EnableNFTables": true, "Backend": backend},
			"fd00:10:96::a", "IPv6"},
	} {
		t.Run(name, func(t *testing.T) {
			c := testCluster(tc.families...)
			c.Flannel = &kubernetes.Flannel{}
			objects, err := Render(c)
			if err != nil {
				t.Fatal(err)
			}
			proxy := data(t, find(t, objects, "ConfigMap", "kube-proxy"), "config.conf")
			if proxy["mode"] != "nftables" || proxy["clusterCIDR"] != tc.clusterCIDR || proxy["bindAddress"] != tc.bind ||
				!reflect.DeepEqual(proxy["nodePortAddresses"], []any{"primary"}) {
				t.Errorf("kube-proxy = %v", proxy)
			}
			if net := data(t, find(t, objects, "ConfigMap", "kube-flannel-cfg"), "net-conf.json"); !reflect.DeepEqual(net, tc.netConf) {
				t.Errorf("net-conf.json = %v, want %v", net, tc.netConf)
			}
			dns := asJSON(t, find(t, objects, "Service", "kube-dns"))["spec"].(map[string]any)
			if dns["clusterIP"] != tc.dnsIP || dns["ipFamilyPolicy"] != "SingleStack" || !reflect.DeepEqual(dns["ipFamilies"], []any{tc.dnsFamily}) {
				t.Errorf("kube-dns = %v", dns)
			}
		})
	}
}

// TestRenderFlannelDaemonSet: flanneld uses the pod's addresses, which are the node's as the
// kubelet registered them, split on commas alone, and finds its interface by them; portmap
// programs nftables; kube-proxy runs the image's release.
func TestRenderFlannelDaemonSet(t *testing.T) {
	objects, err := Render(testCluster("ipv4", "ipv6"))
	if err != nil {
		t.Fatal(err)
	}
	podSpec := func(name string) map[string]any {
		ds := asJSON(t, find(t, objects, "DaemonSet", name))
		return ds["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	}
	flanneld := podSpec("kube-flannel-ds")["containers"].([]any)[0].(map[string]any)
	var podIPs any
	for _, e := range flanneld["env"].([]any) {
		if e.(map[string]any)["name"] == "POD_IPS" {
			podIPs = e.(map[string]any)["valueFrom"]
		}
	}
	if !reflect.DeepEqual(podIPs, map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIPs"}}) {
		t.Errorf("POD_IPS = %v", podIPs)
	}
	command := flanneld["command"].([]any)
	script := command[len(command)-1].(string)
	for _, want := range []string{"--public-ip=$ip", "--public-ipv6=$ip", "set -f\nIFS=,\nfor ip in $POD_IPS"} {
		if !strings.Contains(script, want) {
			t.Errorf("flanneld's script lacks %q:\n%s", want, script)
		}
	}
	for _, arg := range command {
		if strings.Contains(arg.(string), "--iface") {
			t.Errorf("flanneld names its interface: %v", command)
		}
	}
	var portmap any
	for _, p := range data(t, find(t, objects, "ConfigMap", "kube-flannel-cfg"), "cni-conf.json")["plugins"].([]any) {
		if p.(map[string]any)["type"] == "portmap" {
			portmap = p.(map[string]any)["backend"]
		}
	}
	if portmap != "nftables" {
		t.Errorf("portmap's backend = %v", portmap)
	}
	if image := podSpec("kube-proxy")["containers"].([]any)[0].(map[string]any)["image"]; image != "proxy" {
		t.Errorf("kube-proxy's image = %v", image)
	}
}

// TestRenderBindsChalkosIdentities: chalkos's ClusterRoleBindings bind the identities chalkos
// issues certificates and kubeconfigs for.
func TestRenderBindsChalkosIdentities(t *testing.T) {
	objects, err := Render(testCluster("ipv4"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, o := range objects {
		j := asJSON(t, o)
		name := j["metadata"].(map[string]any)["name"].(string)
		if j["kind"] == "ClusterRoleBinding" && strings.HasPrefix(name, "chalkos:") {
			got[name] = j["subjects"].([]any)[0].(map[string]any)["name"]
		}
	}
	want := map[string]any{
		"chalkos:cluster-admins":    "chalkos:cluster-admins",
		"chalkos:selfnodeclient":    "system:nodes",
		"chalkos:apiserver-kubelet": "chalkos:kube-apiserver-kubelet-client",
		"chalkos:node-proxier":      "kube-proxy",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bindings = %v, want %v", got, want)
	}
}

// TestRevokedNodesPolicy: the policy names the revoked nodes inline and takes no parameters, so
// it never waits for an informer; it looks at every write by a node and fails closed. The binding
// has no parameters either.
func TestRevokedNodesPolicy(t *testing.T) {
	objects := RevokedNodes(revoked.List{
		"w2": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC),
		"w1": {},
	})
	if len(objects) != 2 {
		t.Fatalf("objects = %v", objects)
	}
	policy := asJSON(t, find(t, objects, "ValidatingAdmissionPolicy", "chalkos-revoked-nodes"))
	if policy["apiVersion"] != "admissionregistration.k8s.io/v1" {
		t.Errorf("policy's apiVersion = %v", policy["apiVersion"])
	}
	spec := policy["spec"].(map[string]any)
	wantSpec := map[string]any{
		"failurePolicy": "Fail",
		"matchConstraints": map[string]any{"resourceRules": []any{map[string]any{
			"apiGroups":   []any{"*"},
			"apiVersions": []any{"*"},
			"resources":   []any{"*/*"},
			"operations":  []any{"CREATE", "UPDATE", "DELETE", "CONNECT"},
		}}},
		"matchConditions": []any{map[string]any{"name": "node", "expression": `request.userInfo.username.startsWith("system:node:")`}},
		"variables": []any{
			map[string]any{"name": "revoked", "expression": `{"w1": "at an unknown time", "w2": "since 2026-10-23T10:00:00Z"}`},
			map[string]any{"name": "node", "expression": "request.userInfo.username.substring(12)"},
		},
	}
	for field, want := range wantSpec {
		if !reflect.DeepEqual(spec[field], want) {
			t.Errorf("policy's %s = %v, want %v", field, spec[field], want)
		}
	}
	if _, ok := spec["paramKind"]; ok {
		t.Errorf("the policy takes parameters: %v", spec["paramKind"])
	}
	validations := spec["validations"].([]any)
	if len(validations) != 1 || validations[0].(map[string]any)["reason"] != "Forbidden" {
		t.Errorf("policy's validations = %v", validations)
	}
	binding := asJSON(t, find(t, objects, "ValidatingAdmissionPolicyBinding", "chalkos-revoked-nodes"))
	wantBinding := map[string]any{"policyName": "chalkos-revoked-nodes", "validationActions": []any{"Deny"}}
	if !reflect.DeepEqual(binding["spec"], wantBinding) {
		t.Errorf("binding's spec = %v, want %v", binding["spec"], wantBinding)
	}
	// With no node revoked the map is empty and denies nothing.
	empty := asJSON(t, find(t, RevokedNodes(nil), "ValidatingAdmissionPolicy", "chalkos-revoked-nodes"))
	if got := empty["spec"].(map[string]any)["variables"].([]any)[0].(map[string]any)["expression"]; got != "{}" {
		t.Errorf("no revoked nodes: %v, want {}", got)
	}
}

// TestRevokedNodesPolicyEvaluates evaluates the policy's expressions as the API server does, in
// CEL with the Kubernetes strings library and optional types: a revoked node is denied with its
// revocation time, any other node is admitted, and a name that needs escaping stays one string.
func TestRevokedNodesPolicyEvaluates(t *testing.T) {
	odd := "w\"3\\\nä\x00} "
	spec := asJSON(t, find(t, RevokedNodes(revoked.List{
		"w1": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC),
		"w2": {},
		odd:  time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
	}), "ValidatingAdmissionPolicy", "chalkos-revoked-nodes"))["spec"].(map[string]any)
	expr := func(list []any, i int, field string) string { return list[i].(map[string]any)[field].(string) }
	match := expr(spec["matchConditions"].([]any), 0, "expression")
	list := expr(spec["variables"].([]any), 0, "expression")
	node := expr(spec["variables"].([]any), 1, "expression")
	validation := expr(spec["validations"].([]any), 0, "expression")
	message := expr(spec["validations"].([]any), 0, "messageExpression")

	env, err := cel.NewEnv(
		ext.Strings(ext.StringsVersion(2)),
		cel.OptionalTypes(),
		cel.Variable("request", cel.DynType),
		cel.Variable("variables", cel.DynType),
	)
	if err != nil {
		t.Fatal(err)
	}
	eval := func(t *testing.T, src string, vars map[string]any) (ref.Val, error) {
		t.Helper()
		ast, iss := env.Compile(src)
		if iss.Err() != nil {
			t.Fatalf("compile %s: %v", src, iss.Err())
		}
		prg, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := prg.Eval(vars)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	revokedNodes, err := eval(t, list, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		username string
		matched  bool
		admitted bool
		message  string
	}{
		"revoked node":           {"system:node:w1", true, false, "node w1 is revoked since 2026-10-23T10:00:00Z"},
		"revoked at a bad time":  {"system:node:w2", true, false, "node w2 is revoked at an unknown time"},
		"name that needs quotes": {"system:node:" + odd, true, false, "node " + odd + " is revoked since 2026-10-24T00:00:00Z"},
		"other node":             {"system:node:w3", true, true, ""},
		"node with a prefix":     {"system:node:w10", true, true, ""},
		"not a node":             {"w1", false, true, ""},
		"service account":        {"system:serviceaccount:kube-system:w1", false, true, ""},
		"node prefix in a group": {"system:nodes", false, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			vars := map[string]any{"request": map[string]any{"userInfo": map[string]any{"username": tc.username}}}
			matched, err := eval(t, match, vars)
			if err != nil || matched.Value() != tc.matched {
				t.Fatalf("matched = %v, %v; want %v", matched, err, tc.matched)
			}
			if !tc.matched {
				return
			}
			n, err := eval(t, node, vars)
			if err != nil {
				t.Fatal(err)
			}
			vars["variables"] = map[string]any{"node": n, "revoked": revokedNodes}
			admitted, err := eval(t, validation, vars)
			if err != nil || admitted.Value() != tc.admitted {
				t.Fatalf("admitted = %v, %v; want %v", admitted, err, tc.admitted)
			}
			if tc.admitted {
				return
			}
			msg, err := eval(t, message, vars)
			if err != nil || msg.Value() != tc.message {
				t.Errorf("message = %v, %v; want %q", msg, err, tc.message)
			}
		})
	}
}

// TestRevokedNodesPolicyEvaluatesEmpty compiles every expression of the policy without revoked
// nodes, the message's too, and admits every node with the empty map.
func TestRevokedNodesPolicyEvaluatesEmpty(t *testing.T) {
	spec := asJSON(t, find(t, RevokedNodes(nil), "ValidatingAdmissionPolicy", "chalkos-revoked-nodes"))["spec"].(map[string]any)
	expr := func(list []any, field string) string { return list[0].(map[string]any)[field].(string) }
	env, err := cel.NewEnv(
		ext.Strings(ext.StringsVersion(2)),
		cel.OptionalTypes(),
		cel.Variable("request", cel.DynType),
		cel.Variable("variables", cel.DynType),
	)
	if err != nil {
		t.Fatal(err)
	}
	programs := map[string]cel.Program{}
	for name, src := range map[string]string{
		"list":       expr(spec["variables"].([]any), "expression"),
		"validation": expr(spec["validations"].([]any), "expression"),
		"message":    expr(spec["validations"].([]any), "messageExpression"),
	} {
		ast, iss := env.Compile(src)
		if iss.Err() != nil {
			t.Fatalf("compile the %s, %s: %v", name, src, iss.Err())
		}
		if programs[name], err = env.Program(ast); err != nil {
			t.Fatal(err)
		}
	}
	list, _, err := programs["list"].Eval(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := list.Value().(map[ref.Val]ref.Val); !ok || len(m) != 0 {
		t.Fatalf("the list evaluates to %v, want an empty map", list.Value())
	}
	vars := map[string]any{"variables": map[string]any{"node": "w1", "revoked": list}}
	admitted, _, err := programs["validation"].Eval(vars)
	if err != nil || admitted.Value() != true {
		t.Errorf("admitted = %v, %v; want true", admitted, err)
	}
	// The message is evaluated for denied nodes alone, which an empty map has none of.
	if _, _, err := programs["message"].Eval(vars); err == nil {
		t.Error("the message evaluated for a node the map does not name")
	}
}

// TestRevokedLength counts the map literal's code points, which the API server limits.
func TestRevokedLength(t *testing.T) {
	if got := RevokedLength(nil); got != 2 {
		t.Errorf("RevokedLength(nil) = %d, want 2", got)
	}
	l := revoked.List{"wä": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)}
	if got, want := RevokedLength(l), len(`{"wä": "since 2026-10-23T10:00:00Z"}`)-1; got != want {
		t.Errorf("RevokedLength = %d, want %d", got, want)
	}
	if MaxRevokedLength >= 100_000 {
		t.Errorf("MaxRevokedLength = %d, want below the API server's 100,000", MaxRevokedLength)
	}
}
