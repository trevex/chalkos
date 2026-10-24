package revoked

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
)

// asJSON is v as the API server receives it, decoded into maps.
func asJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// policySpec is the spec of the policy for the list, as the API server receives it.
func policySpec(t *testing.T, l List) map[string]any {
	t.Helper()
	policy, _ := Policy(l)
	return asJSON(t, policy)["spec"].(map[string]any)
}

// TestPolicy: the policy names the revoked nodes inline and takes no parameters, so
// it never waits for an informer; it looks at every write by a node and fails closed. The binding
// has no parameters either.
func TestPolicy(t *testing.T) {
	p, b := Policy(List{
		"w2": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC),
		"w1": {},
	})
	policy := asJSON(t, p)
	wantMeta := map[string]any{"name": "chalkos-revoked-nodes"}
	if policy["apiVersion"] != "admissionregistration.k8s.io/v1" || policy["kind"] != "ValidatingAdmissionPolicy" ||
		!reflect.DeepEqual(policy["metadata"], wantMeta) {
		t.Errorf("policy is %v %v %v", policy["apiVersion"], policy["kind"], policy["metadata"])
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
	binding := asJSON(t, b)
	if binding["kind"] != "ValidatingAdmissionPolicyBinding" || !reflect.DeepEqual(binding["metadata"], wantMeta) {
		t.Errorf("binding is %v %v", binding["kind"], binding["metadata"])
	}
	wantBinding := map[string]any{"policyName": "chalkos-revoked-nodes", "validationActions": []any{"Deny"}}
	if !reflect.DeepEqual(binding["spec"], wantBinding) {
		t.Errorf("binding's spec = %v, want %v", binding["spec"], wantBinding)
	}
	// With no node revoked the map is empty and denies nothing.
	if got := policySpec(t, nil)["variables"].([]any)[0].(map[string]any)["expression"]; got != "{}" {
		t.Errorf("no revoked nodes: %v, want {}", got)
	}
}

// TestPolicyEvaluates evaluates the policy's expressions as the API server does, in
// CEL with the Kubernetes strings library and optional types: a revoked node is denied with its
// revocation time, any other node is admitted, and a name that needs escaping stays one string.
func TestPolicyEvaluates(t *testing.T) {
	odd := "w\"3\\\nä\x00} "
	spec := policySpec(t, List{
		"w1": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC),
		"w2": {},
		odd:  time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
	})
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

// TestPolicyEvaluatesEmpty compiles every expression of the policy without revoked
// nodes, the message's too, and admits every node with the empty map.
func TestPolicyEvaluatesEmpty(t *testing.T) {
	spec := policySpec(t, nil)
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

// TestLiteralLength counts the map literal's code points, which the API server limits.
func TestLiteralLength(t *testing.T) {
	if got := LiteralLength(nil); got != 2 {
		t.Errorf("LiteralLength(nil) = %d, want 2", got)
	}
	l := List{"wä": time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)}
	if got, want := LiteralLength(l), len(`{"wä": "since 2026-10-23T10:00:00Z"}`)-1; got != want {
		t.Errorf("LiteralLength = %d, want %d", got, want)
	}
	if MaxLiteralLength >= 100_000 {
		t.Errorf("MaxLiteralLength = %d, want below the API server's 100,000", MaxLiteralLength)
	}
}
