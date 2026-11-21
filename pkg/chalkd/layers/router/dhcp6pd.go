package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	// pdLink, pdAddress and pdLength are the forms a link, a delegated prefix's address and its
	// length must have before they reach a record's name.
	pdLink    = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)
	pdAddress = regexp.MustCompile(`^[0-9a-f:]{2,39}$`)
	pdLength  = regexp.MustCompile(`^[0-9]{1,3}$`)
	// pdVia is the link-local address a record routes to.
	pdVia = regexp.MustCompile(`^fe80:[0-9a-f:]*$`)
	// pdNumber is a lease's time or lifetime, in seconds.
	pdNumber = regexp.MustCompile(`^[0-9]+$`)
)

// dhcp6PD is Kea's run_script hook for the hook point hook, run with the leases' variables alone
// in env. It records each prefix a DHCPv6 server delegates in a file <link>_<prefix>_<length> of
// PDDir, readable by every user, that holds the link-local address that asked for it and when
// its lease ends; release, decline or expiry removes it. Values reach a file name only once they
// match the forms of a link, an IPv6 address and a length; chalkd router dhcp6-routes checks them
// again. Other hook points change nothing.
func (p *Plugin) dhcp6PD(hook string, env func(string) string) error {
	link := env("QUERY6_IFACE_NAME")
	if !pdLink.MatchString(link) {
		link = ""
	}
	via := env("QUERY6_REMOTE_ADDR")
	// record is the record of the prefix address/length on the link the request came in on.
	record := func(address, length string) (string, bool) {
		if link == "" || !pdAddress.MatchString(address) || !pdLength.MatchString(length) {
			return "", false
		}
		return filepath.Join(p.PDDir, link+"_"+address+"_"+length), true
	}
	isPD := func(v string) bool { return env(v) == "IA_PD" }
	switch hook {
	case "leases6_committed":
		for i := range count(env("DELETED_LEASES6_SIZE")) {
			lease := "DELETED_LEASES6_AT" + strconv.Itoa(i) + "_"
			if !isPD(lease + "TYPE") {
				continue
			}
			if f, ok := record(env(lease+"ADDRESS"), env(lease+"PREFIX_LEN")); ok {
				removeRecord(f)
			}
		}
		for i := range count(env("LEASES6_SIZE")) {
			lease := "LEASES6_AT" + strconv.Itoa(i) + "_"
			cltt, valid := env(lease+"CLTT"), env(lease+"VALID_LIFETIME")
			if !isPD(lease+"TYPE") || !pdVia.MatchString(via) || !pdNumber.MatchString(cltt) || !pdNumber.MatchString(valid) {
				continue
			}
			f, ok := record(env(lease+"ADDRESS"), env(lease+"PREFIX_LEN"))
			if !ok {
				continue
			}
			from, err1 := strconv.ParseUint(cltt, 10, 63)
			lifetime, err2 := strconv.ParseUint(valid, 10, 63)
			if err1 != nil || err2 != nil {
				continue
			}
			if err := p.writeRecord(f, fmt.Sprintf("%s %d\n", via, from+lifetime)); err != nil {
				return err
			}
		}
	case "lease6_release", "lease6_decline":
		if !isPD("LEASE6_TYPE") {
			return nil
		}
		if f, ok := record(env("LEASE6_ADDRESS"), env("LEASE6_PREFIX_LEN")); ok {
			removeRecord(f)
		}
	case "lease6_expire":
		// An expiry names no link: the prefix's record of any link goes.
		address, length := env("LEASE6_ADDRESS"), env("LEASE6_PREFIX_LEN")
		if !isPD("LEASE6_TYPE") || !pdAddress.MatchString(address) || !pdLength.MatchString(length) {
			return nil
		}
		records, _ := filepath.Glob(filepath.Join(p.PDDir, "*_"+address+"_"+length))
		for _, f := range records {
			removeRecord(f)
		}
	}
	return nil
}

// count is the number of leases a hook's variable gives, 0 for none or one that is no number.
func count(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// removeRecord removes the record f, which may be gone already.
func removeRecord(f string) { _ = os.Remove(f) }

// writeRecord writes the record f whole in PDDir.new, a directory of its own on the same file
// system, then renames it into place, so that chalkd never reads half a record and the path unit
// sees the record once.
func (p *Plugin) writeRecord(f, content string) error {
	tmp, err := os.CreateTemp(p.PDDir+".new", "")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}
