package diagnostics

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// hostnameLabel is a label of a DNS name: letters, digits, underscores and hyphens, without a
	// hyphen at either end, so no name reads as an option.
	hostnameLabel = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)
	// interfaceName is a network interface's name as Linux takes one, up to 15 characters, without
	// characters a command could read as an option or a path.
	interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)
)

// Required tells whether a call must give the parameter.
func (p Parameter) Required() bool { return !p.Optional && p.Default == "" }

// check refuses a declaration that does not fit the parameter's type.
func (p Parameter) check() error {
	if !slices.Contains(parameterTypes, p.Type) {
		return fmt.Errorf("its type is %q, not one of %s", p.Type, strings.Join(parameterTypes, ", "))
	}
	if (p.Min != nil || p.Max != nil) && p.Type != TypeInt {
		return errors.New("only an int parameter has min and max")
	}
	if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
		return fmt.Errorf("its min %d is above its max %d", *p.Min, *p.Max)
	}
	if (len(p.Values) > 0) != (p.Type == TypeEnum) {
		return errors.New("an enum parameter has values, and no other does")
	}
	for _, v := range p.Values {
		if err := argument(v); err != nil {
			return fmt.Errorf("its value %w", err)
		}
	}
	if (p.Regex != "") != (p.Type == TypePattern) {
		return errors.New("a pattern parameter has a regex, and no other does")
	}
	if p.Type == TypePattern {
		if !strings.HasPrefix(p.Regex, "^") || !strings.HasSuffix(p.Regex, "$") {
			return fmt.Errorf("its regex %q is not anchored with ^ and $", p.Regex)
		}
		if _, err := p.whole(); err != nil {
			return fmt.Errorf("its regex: %w", err)
		}
	}
	if p.Default != "" {
		if p.Optional {
			return errors.New("it is optional and has a default; a default fills the arguments that name it, optional drops them")
		}
		if _, err := p.value(p.Default); err != nil {
			return fmt.Errorf("its default: %w", err)
		}
	}
	return nil
}

// value checks s against the parameter's type and returns the form the command gets, which never
// reads as an option and holds no control character.
func (p Parameter) value(s string) (string, error) {
	v, err := p.typed(s)
	if err != nil {
		return "", err
	}
	if err := argument(v); err != nil {
		return "", err
	}
	return v, nil
}

// typed checks s against the parameter's type and returns its canonical form.
func (p Parameter) typed(s string) (string, error) {
	switch p.Type {
	case TypeAddress:
		a, err := netip.ParseAddr(s)
		if err != nil {
			return "", fmt.Errorf("%q is no IPv4 or IPv6 address", s)
		}
		if a.Zone() != "" && !interfaceName.MatchString(a.Zone()) {
			return "", fmt.Errorf("%q has a zone that is no interface name", s)
		}
		return a.String(), nil
	case TypePrefix:
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return "", fmt.Errorf("%q is no IPv4 or IPv6 prefix, such as 192.0.2.0/24", s)
		}
		return prefix.String(), nil
	case TypeHostname:
		name := strings.TrimSuffix(s, ".")
		if name == "" || len(name) > 253 {
			return "", fmt.Errorf("%q is no DNS name", s)
		}
		for _, label := range strings.Split(name, ".") {
			if !hostnameLabel.MatchString(label) {
				return "", fmt.Errorf("%q is no DNS name", s)
			}
		}
		// Fully qualified, so no search list applies, and a program that tells a name from a
		// record type or class by its dot, as drill does, reads a single label as a name.
		return name + ".", nil
	case TypeInterface:
		if !interfaceName.MatchString(s) {
			return "", fmt.Errorf("%q is no interface name", s)
		}
		return s, nil
	case TypeInt:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q is no integer", s)
		}
		if p.Min != nil && n < *p.Min || p.Max != nil && n > *p.Max {
			return "", fmt.Errorf("%d is not %s", n, p.bounds())
		}
		return strconv.FormatInt(n, 10), nil
	case TypeEnum:
		if !slices.Contains(p.Values, s) {
			return "", fmt.Errorf("%q is not one of %s", s, strings.Join(p.Values, ", "))
		}
		return s, nil
	case TypePattern:
		re, err := p.whole()
		if err != nil {
			return "", fmt.Errorf("its regex: %w", err)
		}
		if !re.MatchString(s) {
			return "", fmt.Errorf("%q does not match %s", s, p.Regex)
		}
		return s, nil
	}
	return "", fmt.Errorf("the type %q is unknown", p.Type)
}

// whole is a pattern parameter's regex made to match the whole value, whatever alternatives it
// holds.
func (p Parameter) whole() (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + p.Regex + ")$")
}

// argument refuses a value that a program could read as an option, or whose control characters
// could break a line of its output or end the argument early, whatever the parameter's
// declaration allows.
func argument(s string) error {
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q starts with \"-\"", s)
	}
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%q holds a control character", s)
	}
	return nil
}

// bounds says which integers an int parameter with a bound takes.
func (p Parameter) bounds() string {
	switch {
	case p.Min != nil && p.Max != nil:
		return fmt.Sprintf("from %d to %d", *p.Min, *p.Max)
	case p.Min != nil:
		return fmt.Sprintf("at least %d", *p.Min)
	default:
		return fmt.Sprintf("at most %d", *p.Max)
	}
}

// values are a call's arguments by parameter, checked and in the form their type gives them, with
// the defaults of those it leaves out; an optional parameter it leaves out has none.
func (d Definition) values(args map[string]string) (map[string]string, error) {
	for _, name := range slices.Sorted(maps.Keys(args)) {
		if _, ok := d.Parameters[name]; !ok {
			return nil, fmt.Errorf("it has no parameter %s; %s", name, d.takes())
		}
	}
	values := map[string]string{}
	for _, name := range d.ParameterNames() {
		p := d.Parameters[name]
		v, given := args[name]
		switch {
		case !given && p.Default != "":
			v = p.Default
		case !given && p.Optional:
			continue
		case !given:
			return nil, fmt.Errorf("it needs %s=<%s>", name, p.Type)
		}
		v, err := p.value(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		values[name] = v
	}
	return values, nil
}

// Argv checks a call's arguments, by parameter name, and returns the command line to run. Each
// placeholder becomes its argument, in the form its type gives it, or the parameter's default. An
// argument of the command that names an optional parameter the call leaves out is dropped.
func (d Definition) Argv(args map[string]string) ([]string, error) {
	values, err := d.values(args)
	if err != nil {
		return nil, err
	}
	argv := []string{d.Command[0]}
	for _, arg := range d.Command[1:] {
		dropped := false
		filled := placeholder.ReplaceAllStringFunc(arg, func(m string) string {
			v, ok := values[m[1:len(m)-1]]
			dropped = dropped || !ok
			return v
		})
		if !dropped {
			argv = append(argv, filled)
		}
	}
	return argv, nil
}

// Namespace checks a call's arguments as Argv does and returns the network namespace the call
// runs in, its placeholders filled; empty for the host's network. It refuses a name that is no
// file's name below NamespaceDir, such as one with a slash or of dots alone.
func (d Definition) Namespace(args map[string]string) (string, error) {
	if d.NetworkNamespace == "" {
		return "", nil
	}
	values, err := d.values(args)
	if err != nil {
		return "", err
	}
	ns := placeholder.ReplaceAllStringFunc(d.NetworkNamespace, func(m string) string { return values[m[1:len(m)-1]] })
	if err := checkNamespace(ns); err != nil {
		return "", err
	}
	return ns, nil
}

// checkNamespace refuses a network namespace's name that is no file's name below NamespaceDir.
func checkNamespace(ns string) error {
	if !filledNamespace.MatchString(ns) {
		return fmt.Errorf("the network namespace %q is no name below %s", ns, NamespaceDir)
	}
	return nil
}

// ParameterNames are the parameters a call must give, then the others, each in the order the
// command first names them.
func (d Definition) ParameterNames() []string {
	var required, others []string
	for _, name := range d.named() {
		p, ok := d.Parameters[name]
		switch {
		case !ok:
		case p.Required():
			required = append(required, name)
		default:
			others = append(others, name)
		}
	}
	return append(required, others...)
}

// takes names the parameters a call may give.
func (d Definition) takes() string {
	names := d.ParameterNames()
	if len(names) == 0 {
		return "it takes none"
	}
	return "it takes " + strings.Join(names, ", ")
}
