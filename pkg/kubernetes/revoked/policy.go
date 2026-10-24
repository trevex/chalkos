package revoked

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	admissionv1ac "k8s.io/client-go/applyconfigurations/admissionregistration/v1"
)

// Policy returns the admission policy that denies every write by a node of the list, and its
// binding. A revoked node's kubelet can then no longer update its Node or its pods, renew its
// client certificate through a CSR or ask for a serving certificate.
//
//   - The policy names the nodes inline and takes no parameters. A policy with parameters denies
//     every request it matches, whoever made it, until its informer has read them.
//   - Its failure policy is Fail: when the API server cannot evaluate it, a node's write is denied.
//
// chalkd applies the policy again whenever the list changes.
func Policy(l List) (*admissionv1ac.ValidatingAdmissionPolicyApplyConfiguration, *admissionv1ac.ValidatingAdmissionPolicyBindingApplyConfiguration) {
	policy := admissionv1ac.ValidatingAdmissionPolicy(Name).
		WithSpec(admissionv1ac.ValidatingAdmissionPolicySpec().
			WithFailurePolicy(admissionv1.Fail).
			// Subresources too: the kubelet writes nodes/status and pods/status.
			WithMatchConstraints(admissionv1ac.MatchResources().
				WithResourceRules(admissionv1ac.NamedRuleWithOperations().
					WithAPIGroups("*").
					WithAPIVersions("*").
					WithResources("*/*").
					WithOperations(admissionv1.Create, admissionv1.Update, admissionv1.Delete, admissionv1.Connect))).
			// Requests by anyone but a node end here.
			WithMatchConditions(admissionv1ac.MatchCondition().
				WithName("node").
				WithExpression(`request.userInfo.username.startsWith("system:node:")`)).
			WithVariables(
				admissionv1ac.Variable().WithName("revoked").WithExpression(literal(l)),
				admissionv1ac.Variable().WithName("node").WithExpression("request.userInfo.username.substring(12)"),
			).
			WithValidations(admissionv1ac.Validation().
				WithExpression("!(variables.node in variables.revoked)").
				WithMessageExpression(`"node " + variables.node + " is revoked " + variables.revoked[variables.node]`).
				WithMessage("the node is revoked").
				WithReason("Forbidden")))
	binding := admissionv1ac.ValidatingAdmissionPolicyBinding(Name).
		WithSpec(admissionv1ac.ValidatingAdmissionPolicyBindingSpec().
			WithPolicyName(Name).
			WithValidationActions(admissionv1.Deny))
	return policy, binding
}

// MaxLiteralLength bounds the length of the policy's map of revoked nodes, in code points. The
// API server compiles no CEL expression longer than 100,000 code points. The bound leaves room
// for nodes revoked through other control planes at the same time, each of which checks the list
// as it read it.
const MaxLiteralLength = 90_000

// LiteralLength is the length, in code points, of the map of revoked nodes the policy names for
// the list, which MaxLiteralLength bounds.
func LiteralLength(l List) int {
	return utf8.RuneCountInString(literal(l))
}

// literal is a CEL map from each revoked node's name to when it was revoked, in name order.
// strconv.Quote is safe here: ConfigMap keys are limited to [-._a-zA-Z0-9] and the times are
// rendered again, so no string holds what Go and CEL quote differently. The one mismatch, Go's
// \xNN for bytes of invalid UTF-8, which CEL reads as a code point, cannot occur.
func literal(l List) string {
	var entries []string
	for _, name := range slices.Sorted(maps.Keys(l)) {
		when := "at an unknown time"
		if at := l[name]; !at.IsZero() {
			when = "since " + at.UTC().Format(TimeFormat)
		}
		entries = append(entries, strconv.Quote(name)+": "+strconv.Quote(when))
	}
	return "{" + strings.Join(entries, ", ") + "}"
}
