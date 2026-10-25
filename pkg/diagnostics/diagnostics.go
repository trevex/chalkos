// Package diagnostics reads the diagnostics an image declares in diagnostics.json: read-only
// commands of its store that chalkd runs for a client of the node API. It checks a call's
// arguments against their parameters' types, builds the command line, and runs it as a transient
// systemd unit with a sandbox of its own. chalkd reads the file on the node; chalkos-rules checks
// it when the image is built.
package diagnostics

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/command"
	"github.com/trevex/chalkos/pkg/jsonfile"
)

// DefaultPath is where an image declares its diagnostics.
const DefaultPath = "/etc/chalkos/diagnostics.json"

// MaxOutput is the most chalkd sends of what a run prints, on standard output and error together.
const MaxOutput = 1 << 20

// MaxTimeout bounds a diagnostic's timeout.
const MaxTimeout = 10 * time.Minute

// The types of a parameter.
const (
	TypeAddress   = "address"
	TypePrefix    = "prefix"
	TypeHostname  = "hostname"
	TypeInterface = "interface"
	TypeInt       = "int"
	TypeEnum      = "enum"
	TypePattern   = "pattern"
)

var parameterTypes = []string{TypeAddress, TypePrefix, TypeHostname, TypeInterface, TypeInt, TypeEnum, TypePattern}

// roles are the client roles a diagnostic may need, by the names pki gives them.
var roles = []string{"reader", "operator", "admin"}

// nameForm is the form of a diagnostic's name and a parameter's.
const nameForm = "[a-z][a-z0-9-]*"

// MaxName bounds the length of a diagnostic's name, which its runs' unit names hold.
const MaxName = 64

// groupForm is the form of a group a diagnostic's process belongs to.
const groupForm = "[a-z_][a-z0-9_-]*"

// capabilities are the names of the Linux capabilities.
var capabilities = []string{
	"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL",
	"CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE",
	"CAP_NET_BROADCAST", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER",
	"CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE", "CAP_SYS_PACCT",
	"CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME",
	"CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL",
	"CAP_SETFCAP", "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM",
	"CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE",
}

var (
	namePattern = regexp.MustCompile("^" + nameForm + "$")
	// placeholder is a parameter's place in an argument of a command.
	placeholder  = regexp.MustCompile(`\{(` + nameForm + `)\}`)
	groupPattern = regexp.MustCompile("^" + groupForm + "$")
	// timeoutPattern is the form of a timeout: seconds or minutes, as the image's option takes it.
	timeoutPattern = regexp.MustCompile(`^[1-9][0-9]*[sm]$`)
)

// Definition is a diagnostic as diagnostics.json records it: the command chalkd runs, the
// parameters a call fills its placeholders with, the least client role that runs it, how long
// it may run, and the capabilities and groups its process gets.
type Definition struct {
	Description  string               `json:"description"`
	Command      []string             `json:"command"`
	Parameters   map[string]Parameter `json:"parameters"`
	Role         string               `json:"role"`
	Timeout      string               `json:"timeout"`
	Capabilities []string             `json:"capabilities"`
	Groups       []string             `json:"groups"`
}

// Parameter is a value a call passes a diagnostic: of Type, from Min to Max for an int, one of
// Values for an enum, matching Regex whole for a pattern. A call may leave out a parameter that
// is Optional, which drops the arguments that name it, or that has a Default, which fills them.
type Parameter struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Optional    bool     `json:"optional"`
	Default     string   `json:"default"`
	Min         *int64   `json:"min"`
	Max         *int64   `json:"max"`
	Values      []string `json:"values"`
	Regex       string   `json:"regex"`
}

// Read reads the image's diagnostics.json, refusing a diagnostic chalkd could not run; an image
// without one declares none.
func Read(path string) (map[string]Definition, error) {
	defs := map[string]Definition{}
	if err := jsonfile.Read(path, &defs); err != nil {
		return nil, err
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		err := defs[name].Check()
		switch {
		case !namePattern.MatchString(name):
			err = fmt.Errorf("the name is not of the form %s", nameForm)
		case len(name) > MaxName:
			err = fmt.Errorf("the name is longer than %d characters", MaxName)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: diagnostic %s: %w", path, name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

// Check refuses a diagnostic chalkd could not run as declared: a command that runs no program
// of the image's store, a role, timeout, capability or group chalkd does not know, a placeholder
// without its parameter or a parameter without its placeholder, and a parameter whose declaration
// does not fit its type.
func (d Definition) Check() error {
	if len(d.Command) == 0 {
		return errors.New("its command is empty")
	}
	if !command.InStore(d.Command[0]) || placeholder.MatchString(d.Command[0]) {
		return fmt.Errorf("its command runs %s, which is no program of the image's store", d.Command[0])
	}
	if !slices.Contains(roles, d.Role) {
		return fmt.Errorf("its role is %q, not reader, operator or admin", d.Role)
	}
	if t, err := time.ParseDuration(d.Timeout); !timeoutPattern.MatchString(d.Timeout) || err != nil || t < time.Second || t > MaxTimeout {
		return fmt.Errorf("its timeout is %q, not a duration from 1s to 10m", d.Timeout)
	}
	for _, c := range d.Capabilities {
		if !slices.Contains(capabilities, c) {
			return fmt.Errorf("%q is no capability, such as CAP_NET_RAW", c)
		}
	}
	for _, g := range d.Groups {
		if !groupPattern.MatchString(g) {
			return fmt.Errorf("group %q is not of the form %s", g, groupForm)
		}
	}
	named := d.named()
	for _, p := range named {
		if _, ok := d.Parameters[p]; !ok {
			return fmt.Errorf("its command names {%s}, which is no parameter", p)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(d.Parameters)) {
		if !namePattern.MatchString(p) {
			return fmt.Errorf("parameter %q: the name is not of the form %s", p, nameForm)
		}
		if !slices.Contains(named, p) {
			return fmt.Errorf("parameter %s: its command names no {%s}", p, p)
		}
		if err := d.Parameters[p].check(); err != nil {
			return fmt.Errorf("parameter %s: %w", p, err)
		}
	}
	return nil
}

// RunTime is how long a run may take: Timeout, which Check accepted.
func (d Definition) RunTime() time.Duration {
	t, _ := time.ParseDuration(d.Timeout)
	return t
}

// named are the parameters the command's arguments name, in the order they first name them.
func (d Definition) named() []string {
	var names []string
	for _, arg := range d.Command[1:] {
		for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
			if !slices.Contains(names, m[1]) {
				names = append(names, m[1])
			}
		}
	}
	return names
}
