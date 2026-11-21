package chalkd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// auditCmd is the start of the command that reads the boot's audit records of IPE.
const auditCmd = "journalctl --boot --no-pager --output=json"

// writeIPE lays out securityfs with the image's policy active and enforced, and the record of
// its load.
func writeIPE(t *testing.T, s *Server, enforce string) {
	t.Helper()
	sec := s.Paths.SecurityFS
	write(t, filepath.Join(sec, "lsm"), "lockdown,capability,ipe,landlock,yama,bpf")
	write(t, filepath.Join(sec, "ipe/enforce"), enforce)
	write(t, filepath.Join(sec, "ipe/policies/chalkos/active"), "1")
	write(t, filepath.Join(sec, "ipe/policies/chalkos/version"), "0.5.0")
	write(t, filepath.Join(sec, "ipe/policies/chalkos/pkcs7"), "blob")
	// sha256("blob")
	write(t, s.Paths.ModulePolicy, `{"state":"loaded","version":"0.5.0","digest":"fa2c8cc4f28176bbeed4b736df569a34c79cd3723e9ec42f9674b4d46ac6b8b8"}`)
}

// modulePolicyOf is the module policy's state as Info reports it.
func modulePolicyOf(t *testing.T, s *Server) *nodev1.ModulePolicyStatus {
	t.Helper()
	resp, err := s.Info(context.Background(), &nodev1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return resp.ModulePolicy
}

func TestInfoModulePolicy(t *testing.T) {
	s, r := newTestServer(t, normal, vda)
	write(t, filepath.Join(s.Paths.EFIVars, "SecureBoot-"+secureboot.GlobalVendor), "\x06\x00\x00\x00\x01")
	writeIPE(t, s, "1")
	r.Rules = []tooltest.Rule{{Prefix: auditCmd, Out: ""}}
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_ENFORCED || st.Version != "0.5.0" {
		t.Fatalf("enforced: %+v", st)
	}
	write(t, filepath.Join(s.Paths.SecurityFS, "ipe/policies/chalkos/pkcs7"), "other")
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED || !strings.Contains(st.Detail, "not the image's") {
		t.Errorf("another blob: %+v", st)
	}
	writeIPE(t, s, "0")
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED {
		t.Errorf("permissive: %+v", st)
	}
	writeIPE(t, s, "1")
	r.Rules = []tooltest.Rule{{Prefix: auditCmd, Out: `{"_AUDIT_TYPE":"1404","MESSAGE":"enforcing=0 old_enforcing=1 lsm=ipe res=1"}` + "\n"}}
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED || !strings.Contains(st.Detail, "switched to permissive") {
		t.Errorf("switched off and on: %+v", st)
	}
	// The record of a load the kernel refused, with Secure Boot on.
	write(t, s.Paths.ModulePolicy, `{"state":"failed","error":"the kernel refused the module policy: required key not available"}`)
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED || !strings.Contains(st.Detail, "required key") {
		t.Errorf("refused: %+v", st)
	}
}

// A node without Secure Boot reports the policy as not enforced, in maintenance mode too.
func TestInfoModulePolicyWithoutSecureBoot(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	write(t, filepath.Join(s.Paths.SecurityFS, "lsm"), "capability,ipe,bpf")
	write(t, filepath.Join(s.Paths.SecurityFS, "ipe/enforce"), "1")
	write(t, s.Paths.ModulePolicy, `{"state":"unsigned","error":"the image is not signed"}`)
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_NOT_ENFORCED || st.Detail == "" {
		t.Errorf("%+v", st)
	}
}

// An image without IPE reports the policy as off; securityfs that cannot be read as failed.
func TestInfoModulePolicyOffOrUnreadable(t *testing.T) {
	s, _ := newTestServer(t, normal, vda)
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED || st.Detail == "" {
		t.Errorf("no securityfs: %+v", st)
	}
	write(t, filepath.Join(s.Paths.SecurityFS, "lsm"), "capability,landlock,yama,bpf")
	if st := modulePolicyOf(t, s); st.State != nodev1.ModulePolicyState_MODULE_POLICY_STATE_OFF {
		t.Errorf("no IPE: %+v", st)
	}
}
