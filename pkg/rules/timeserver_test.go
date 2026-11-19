package rules

import (
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The TimeServer rules accept a server of the fabric with its own clock and refuse an address it
// cannot listen on and listen and allow without a family in common.
func TestTimeServerRules(t *testing.T) {
	set, doc := routerRules(t)
	server := func(spec string) []config.Document { return []config.Document{doc("TimeServer", "ntp", spec)} }
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a server of the fabric", server(`{"listen": ["2001:db8:1::1", "192.0.2.1"], "allow": ["2001:db8::/32", "192.0.2.0/24"], "localStratum": 10}`), nil},
		{"a link-local address", server(`{"listen": ["fe80::1"], "allow": ["2001:db8::/32"]}`),
			[]string{`TimeServer/ntp: spec.listen: the address fe80::1 is no unicast address the time server can listen on`}},
		{"a loopback address", server(`{"listen": ["127.0.0.1"], "allow": ["192.0.2.0/24"]}`),
			[]string{`TimeServer/ntp: spec.listen: the address 127.0.0.1 is no unicast address the time server can listen on`}},
		{"listen and allow of other families", server(`{"listen": ["2001:db8:1::1"], "allow": ["192.0.2.0/24"]}`),
			[]string{"TimeServer ntp listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common"}},
	} {
		t.Run(tc.name, func(t *testing.T) { wantFailures(t, set, Input{Documents: config.Redact(tc.docs)}, tc.want) })
	}
}
