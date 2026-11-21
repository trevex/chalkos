package modulepolicy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Verdict is what chalkd reports of the module policy.
type Verdict string

const (
	// VerdictEnforced: the image's policy is active and enforced, and nothing changed that this boot.
	VerdictEnforced Verdict = "enforced"
	// VerdictNotEnforced: the node has no Secure Boot, and the kernel did not take the policy.
	VerdictNotEnforced Verdict = "not enforced"
	// VerdictFailed: Secure Boot is on, but the policy did not load.
	VerdictFailed Verdict = "failed"
	// VerdictTampered: the policy loaded, but enforcement was switched off or another policy
	// activated since.
	VerdictTampered Verdict = "tampered"
	// VerdictOff: the image leaves the module policy out.
	VerdictOff Verdict = "off"
)

// Policy is a policy as securityfs shows it.
type Policy struct {
	Name, Version string
	// Digest is the SHA-256 of the signed policy the kernel holds, in lower-case hex.
	Digest string
}

// Audit counts the boot's audit records of changes to IPE.
type Audit struct {
	// EnforceOff counts the times IPE was switched to permissive (MAC_STATUS enforcing=0).
	EnforceOff int
	// SwitchedAway counts the activations of another policy while the image's was active
	// (IPE_CONFIG_CHANGE from chalkos).
	SwitchedAway int
}

// Observed is what chalkd reads of the module policy.
type Observed struct {
	// IPE is whether the kernel runs IPE, as securityfs's lsm lists it.
	IPE bool
	// Record is the initrd loader's record; nil without one.
	Record *Record
	// SecureBoot is whether the firmware runs Secure Boot.
	SecureBoot bool
	// Enforce is IPE's enforce file, trimmed: 1 or 0.
	Enforce string
	// Active is the active policy; nil without one.
	Active *Policy
	Audit  Audit
}

// Status is the verdict and why.
type Status struct {
	Verdict Verdict
	// Detail says why the policy is not enforced, failed or was tampered with.
	Detail string
	// Version is the image's policy_version once the kernel took it.
	Version string
}

// Observe reads IPE's securityfs and the loader's record. SecureBoot and Audit are the caller's.
func Observe(p Paths) (Observed, error) {
	var o Observed
	lsm, err := os.ReadFile(filepath.Join(p.SecurityFS, "lsm"))
	if err != nil {
		return o, fmt.Errorf("read the kernel's LSMs: %w", err)
	}
	o.IPE = slices.Contains(strings.Split(strings.TrimSpace(string(lsm)), ","), "ipe")
	if !o.IPE {
		return o, nil
	}
	switch b, err := os.ReadFile(p.Record); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return o, err
	default:
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			return o, fmt.Errorf("parse %s: %w", p.Record, err)
		}
		o.Record = &r
	}
	ipe := filepath.Join(p.SecurityFS, "ipe")
	enforce, err := os.ReadFile(filepath.Join(ipe, "enforce"))
	if err != nil {
		return o, err
	}
	o.Enforce = strings.TrimSpace(string(enforce))
	dirs, err := os.ReadDir(filepath.Join(ipe, "policies"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return o, err
	}
	for _, d := range dirs {
		dir := filepath.Join(ipe, "policies", d.Name())
		if active, err := os.ReadFile(filepath.Join(dir, "active")); err != nil || strings.TrimSpace(string(active)) != "1" {
			continue
		}
		version, err := os.ReadFile(filepath.Join(dir, "version"))
		if err != nil {
			return o, err
		}
		pkcs7, err := os.ReadFile(filepath.Join(dir, "pkcs7"))
		if err != nil {
			return o, err
		}
		sum := sha256.Sum256(pkcs7)
		o.Active = &Policy{Name: d.Name(), Version: strings.TrimSpace(string(version)), Digest: hex.EncodeToString(sum[:])}
	}
	return o, nil
}

// Evaluate decides the verdict.
func Evaluate(o Observed) Status {
	if !o.IPE {
		return Status{Verdict: VerdictOff}
	}
	if o.Record == nil || o.Record.State != StateLoaded {
		detail := "the initrd left no record of the module policy"
		if o.Record != nil {
			detail = o.Record.Error
		}
		if o.SecureBoot {
			return Status{Verdict: VerdictFailed, Detail: detail}
		}
		return Status{Verdict: VerdictNotEnforced, Detail: detail}
	}
	s := Status{Verdict: VerdictTampered, Version: o.Record.Version}
	want := Policy{Name: Name, Version: o.Record.Version, Digest: o.Record.Digest}
	switch {
	case o.Enforce != "1":
		s.Detail = "IPE is permissive"
	case o.Active == nil:
		s.Detail = "no policy is active"
	case *o.Active != want:
		s.Detail = fmt.Sprintf("the active policy is %s %s, not the image's", o.Active.Name, o.Active.Version)
	case o.Audit.EnforceOff > 0:
		s.Detail = fmt.Sprintf("IPE was switched to permissive %d times during this boot", o.Audit.EnforceOff)
	case o.Audit.SwitchedAway > 0:
		s.Detail = fmt.Sprintf("another policy was activated %d times during this boot", o.Audit.SwitchedAway)
	default:
		s.Verdict = VerdictEnforced
	}
	return s
}

// CountAudit counts IPE's changes in the boot's audit records, as journalctl --output=json
// prints those of _AUDIT_TYPE 1404 (MAC_STATUS) and 1421 (IPE_CONFIG_CHANGE).
func CountAudit(journal []byte) (Audit, error) {
	var a Audit
	sc := bufio.NewScanner(bytes.NewReader(journal))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e struct {
			Type    string `json:"_AUDIT_TYPE"`
			Message string `json:"MESSAGE"`
		}
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return a, fmt.Errorf("parse the journal's audit records: %w", err)
		}
		f := fields(e.Message)
		if f["lsm"] != "ipe" {
			continue
		}
		switch e.Type {
		case "1404":
			if f["enforcing"] == "0" {
				a.EnforceOff++
			}
		case "1421":
			if f["old_active_pol_name"] == Name {
				a.SwitchedAway++
			}
		}
	}
	return a, sc.Err()
}

// fields splits an audit record into its key=value fields, without the quotes of a value.
func fields(msg string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Fields(msg) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = strings.Trim(v, `"`)
		}
	}
	return m
}
