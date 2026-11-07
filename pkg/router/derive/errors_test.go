package derive

import (
	"errors"
	"fmt"
	"testing"

	"github.com/trevex/chalkos/pkg/refusals"
)

func TestWithin(t *testing.T) {
	internal := &internalError{"broken"}
	refusal := refusals.Errorf("Network", "lan", "spec.prefix", "is no prefix")
	other := errors.New("failed")
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"internal", internal, "internal error: Network lan: broken"},
		{"wrapped internal", fmt.Errorf("render: %w", internal), "Network lan: render: internal error: broken"},
		{"joined internal", errors.Join(internal, other), "Network lan: internal error: broken\nfailed"},
		{"refusal", refusal, refusal.Error()},
		{"other", other, "Network lan: failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Within("Network lan", tc.err).Error(); got != tc.want {
				t.Errorf("Within = %q, want %q", got, tc.want)
			}
		})
	}
}
