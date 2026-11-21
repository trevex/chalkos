package chalkd

import (
	"context"
	"log"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/modulepolicy"
	"github.com/trevex/chalkos/pkg/tool"
)

// modulePolicyStates maps the verdicts to the API's states.
var modulePolicyStates = map[modulepolicy.Verdict]nodev1.ModulePolicyState{
	modulepolicy.VerdictEnforced:    nodev1.ModulePolicyState_MODULE_POLICY_STATE_ENFORCED,
	modulepolicy.VerdictNotEnforced: nodev1.ModulePolicyState_MODULE_POLICY_STATE_NOT_ENFORCED,
	modulepolicy.VerdictFailed:      nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED,
	modulepolicy.VerdictTampered:    nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED,
	modulepolicy.VerdictOff:         nodev1.ModulePolicyState_MODULE_POLICY_STATE_OFF,
}

// modulePolicyStatus reads the module policy's state: securityfs, the initrd's record, Secure
// Boot and the boot's audit records of IPE. What it cannot read makes the state failed, with the
// reason.
func (s *Server) modulePolicyStatus(ctx context.Context) *nodev1.ModulePolicyStatus {
	o, err := modulepolicy.Observe(modulepolicy.Paths{SecurityFS: s.Paths.SecurityFS, Record: s.Paths.ModulePolicy})
	if err != nil {
		return &nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED, Detail: err.Error()}
	}
	o.SecureBoot = s.secureBoot() == nodev1.SecureBoot_SECURE_BOOT_ENABLED
	if o.IPE && o.Record != nil && o.Record.State == modulepolicy.StateLoaded {
		// journalctl ANDs different fields and ORs the values of one: the audit records of type
		// MAC_STATUS (1404) or IPE_CONFIG_CHANGE (1421).
		out, err := s.Run.Exec(ctx, tool.Cmd{Name: "journalctl", Args: []string{
			"--boot", "--no-pager", "--output=json", "--output-fields=_AUDIT_TYPE,MESSAGE",
			"_TRANSPORT=audit", "_AUDIT_TYPE=1404", "_AUDIT_TYPE=1421",
		}, Quiet: true})
		if err == nil {
			o.Audit, err = modulepolicy.CountAudit(out)
		}
		if err != nil {
			log.Printf("read the boot's audit records of IPE: %v", err)
			return &nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED, Detail: "the boot's audit records cannot be read: " + err.Error()}
		}
	}
	st := modulepolicy.Evaluate(o)
	return &nodev1.ModulePolicyStatus{State: modulePolicyStates[st.Verdict], Detail: st.Detail, Version: st.Version}
}
