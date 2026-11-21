package modulepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestEvaluate(t *testing.T) {
	loaded := &Record{State: StateLoaded, Version: "0.4.0", Digest: "aa"}
	active := &Policy{Name: Name, Version: "0.4.0", Digest: "aa"}
	enforced := Observed{IPE: true, Record: loaded, SecureBoot: true, Enforce: "1", Active: active}
	with := func(f func(*Observed)) Observed { o := enforced; f(&o); return o }
	for name, tc := range map[string]struct {
		in   Observed
		want Verdict
	}{
		"enforced":            {enforced, VerdictEnforced},
		"enforced without SB": {with(func(o *Observed) { o.SecureBoot = false }), VerdictEnforced},
		"off":                 {Observed{}, VerdictOff},
		"unsigned, no SB":     {Observed{IPE: true, Record: &Record{State: StateUnsigned}}, VerdictNotEnforced},
		"refused, no SB":      {Observed{IPE: true, Record: &Record{State: StateFailed, Error: "x"}}, VerdictNotEnforced},
		"no record, SB":       {Observed{IPE: true, SecureBoot: true}, VerdictFailed},
		"refused, SB":         {Observed{IPE: true, SecureBoot: true, Record: &Record{State: StateFailed, Error: "x"}}, VerdictFailed},
		"unsigned, SB":        {Observed{IPE: true, SecureBoot: true, Record: &Record{State: StateUnsigned}}, VerdictFailed},
		"permissive":          {with(func(o *Observed) { o.Enforce = "0" }), VerdictTampered},
		"other policy":        {with(func(o *Observed) { o.Active = &Policy{Name: "other", Version: "9.0.0"} }), VerdictTampered},
		"no active policy":    {with(func(o *Observed) { o.Active = nil }), VerdictTampered},
		"other digest":        {with(func(o *Observed) { o.Active = &Policy{Name: Name, Version: "0.4.0", Digest: "bb"} }), VerdictTampered},
		"switched off once":   {with(func(o *Observed) { o.Audit.EnforceOff = 1 }), VerdictTampered},
		"switched away once":  {with(func(o *Observed) { o.Audit.SwitchedAway = 1 }), VerdictTampered},
	} {
		got := Evaluate(tc.in)
		if got.Verdict != tc.want {
			t.Errorf("%s: %+v, want %s", name, got, tc.want)
		}
		if got.Verdict != VerdictEnforced && got.Verdict != VerdictOff && got.Detail == "" && tc.in.Record != nil && tc.in.Record.Error != "" {
			t.Errorf("%s: no detail", name)
		}
	}
}

func TestObserve(t *testing.T) {
	p := fakeIPE(t)
	write(t, filepath.Join(p.SecurityFS, "lsm"), "lockdown,capability,ipe,landlock,yama,bpf,ima")
	write(t, filepath.Join(p.SecurityFS, "ipe/enforce"), "1\n")
	write(t, filepath.Join(p.SecurityFS, "ipe/policies/chalkos/active"), "1\n")
	write(t, filepath.Join(p.SecurityFS, "ipe/policies/chalkos/name"), "chalkos\n")
	write(t, filepath.Join(p.SecurityFS, "ipe/policies/chalkos/pkcs7"), "blob")
	write(t, filepath.Join(p.SecurityFS, "ipe/policies/old/active"), "0\n")
	write(t, p.Record, `{"state":"loaded","version":"0.4.0","digest":"x"}`)
	o, err := Observe(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("blob"))
	if !o.IPE || o.Enforce != "1" || o.Record == nil || o.Record.Version != "0.4.0" ||
		o.Active == nil || *o.Active != (Policy{Name: "chalkos", Version: "0.4.0", Digest: hex.EncodeToString(sum[:])}) {
		t.Errorf("Observe = %+v, active %+v", o, o.Active)
	}
}

func TestCountAudit(t *testing.T) {
	journal := `{"_AUDIT_TYPE":"1404","MESSAGE":"enforcing=0 old_enforcing=1 auid=4294967295 ses=4294967295 enabled=1 old-enabled=1 lsm=ipe res=1"}
{"_AUDIT_TYPE":"1404","MESSAGE":"enforcing=1 old_enforcing=0 auid=4294967295 ses=4294967295 enabled=1 old-enabled=1 lsm=ipe res=1"}
{"_AUDIT_TYPE":"1404","MESSAGE":"enforcing=0 old_enforcing=1 lsm=selinux res=1"}
{"_AUDIT_TYPE":"1421","MESSAGE":"old_active_pol_name=? old_active_pol_version=? old_policy_digest=? new_active_pol_name=\"chalkos\" new_active_pol_version=0.4.0 new_policy_digest=sha256:aa auid=0 ses=1 lsm=ipe res=1"}
{"_AUDIT_TYPE":"1421","MESSAGE":"old_active_pol_name=\"chalkos\" old_active_pol_version=0.4.0 old_policy_digest=sha256:aa new_active_pol_name=\"allowall\" new_active_pol_version=9.0.0 new_policy_digest=sha256:bb auid=0 ses=1 lsm=ipe res=1"}
`
	got, err := CountAudit([]byte(journal))
	if err != nil || got != (Audit{EnforceOff: 1, SwitchedAway: 1}) {
		t.Errorf("CountAudit = %+v, %v", got, err)
	}
}
