// Package frr reloads FRR: it parses frr.conf and FRR's running config into contexts, works out
// the commands that turn the one into the other, and applies them with vtysh while FRR runs, so
// that the sessions the change leaves alone stay up. It knows the commands chalkos's renderers
// write and the lines FRR shows for them, and refuses a config that holds another.
package frr

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A kind is a kind of context: the commands it holds and the contexts they open.
type kind struct {
	name  string
	rules []rule
	// exit is the command that leaves the context.
	exit string
}

// A rule is a command a context holds.
type rule struct {
	re *regexp.Regexp
	// opens is the kind of context the command enters, nil for a command of its context.
	opens *kind
	// slot, expanded with re's groups, names what the command sets: a line of the same slot
	// replaces it, so a change of value is no deletion. Empty, the command is its own slot.
	slot string
	// hidden is the line FRR leaves out of its running config, as it sets FRR's default.
	hidden string
	// ignore marks a line the reload leaves alone on both sides: FRR writes it, or shows another
	// value than frr.conf's.
	ignore bool
	// defines marks the line that creates a neighbour n or a peer group n; deleting it deletes
	// the neighbour and its config.
	defines defines
	// byValue marks an entry of a community-list or as-path access-list, which FRR keys by its
	// value: it refuses a value the list holds already, at any sequence number, and deletes an
	// entry by its value alone. The entry's group list names its list and value its value.
	byValue bool
}

type defines int

const (
	definesNothing defines = iota
	// definesGroup is the line that creates a peer group.
	definesGroup
	// definesMember is the line that creates a neighbour in a peer group.
	definesMember
)

func r(pattern string) rule { return rule{re: regexp.MustCompile("^" + pattern + "$")} }

func (r rule) opening(k *kind) rule { r.opens = k; return r }

func (r rule) slotted(s string) rule { r.slot = s; return r }

func (r rule) hiding(line string) rule { r.hidden = line; return r }

func (r rule) ignored() rule { r.ignore = true; return r }

func (r rule) defining(what defines) rule { r.defines = what; return r }

func (r rule) keyedByValue() rule { r.byValue = true; return r }

// multipath is the number of paths FRR's build installs at most, --enable-multipath, the default
// of maximum-paths, which FRR's running config leaves out.
const multipath = "64"

var (
	addressFamily = &kind{name: "address-family", exit: "exit-address-family", rules: []rule{
		// A route-map of a network, a redistribution or a default route changes in place; one
		// added or removed makes another command.
		r(`network (?P<p>\S+)(?:(?P<rm> route-map) \S+)?`).slotted("network ${p}${rm}"),
		r(`redistribute (?P<t>kernel|connected) route-map \S+`).slotted("redistribute $t"),
		r(`maximum-paths \d+`).slotted("maximum-paths").hiding("maximum-paths " + multipath),
		r(`maximum-paths ibgp \d+`).slotted("maximum-paths ibgp").hiding("maximum-paths ibgp " + multipath),
		r(`neighbor (?P<n>\S+) activate`),
		r(`neighbor (?P<n>\S+) default-originate(?:(?P<rm> route-map) \S+)?`).slotted("neighbor ${n} default-originate${rm}"),
		r(`neighbor (?P<n>\S+) route-map \S+ (?P<d>in|out)`).slotted("neighbor $n route-map $d"),
		r(`neighbor (?P<n>\S+) maximum-prefix \d+`).slotted("neighbor $n maximum-prefix"),
		r(`import vrf route-map \S+`).slotted("import vrf route-map"),
		r(`import vrf \S+`),
	}}
	bgp = &kind{name: "router bgp", exit: "exit", rules: []rule{
		r(`address-family (ipv4|ipv6) unicast`).opening(addressFamily),
		r(`bgp router-id \S+`).slotted("bgp router-id"),
		r(`no bgp default ipv4-unicast`),
		r(`bgp bestpath as-path multipath-relax`),
		r(`bgp graceful-restart`),
		r(`bgp listen range \S+ peer-group \S+`),
		r(`neighbor (?P<n>\S+) peer-group`).defining(definesGroup),
		r(`neighbor (?P<n>\S+) (interface )?peer-group (?P<g>\S+)`).defining(definesMember),
		r(`neighbor (?P<n>\S+) remote-as \S+`).slotted("neighbor $n remote-as"),
		r(`neighbor (?P<n>\S+) description .+`).slotted("neighbor $n description"),
		r(`neighbor (?P<n>\S+) password \S+`).slotted("neighbor $n password"),
		r(`neighbor (?P<n>\S+) bfd`),
		r(`neighbor (?P<n>\S+) bfd profile \S+`).slotted("neighbor $n bfd profile"),
		r(`neighbor (?P<n>\S+) capability extended-nexthop`),
		r(`neighbor (?P<n>\S+) shutdown`),
		r(`neighbor (?P<n>\S+) timers connect \d+`).slotted("neighbor $n timers connect"),
		r(`neighbor (?P<n>\S+) interface \S+`).slotted("neighbor $n interface"),
	}}
	routeMap = &kind{name: "route-map", exit: "exit", rules: []rule{
		r(`match (?P<f>ip|ipv6) address prefix-list \S+`).slotted("match $f address prefix-list"),
		r(`match (?P<k>community|large-community) \S+`).slotted("match $k"),
		r(`match as-path \S+`).slotted("match as-path"),
		r(`match interface \S+`).slotted("match interface"),
		r(`match (ip|ipv6) next-hop type blackhole`),
		r(`set local-preference \d+`).slotted("set local-preference"),
		r(`set metric \d+`).slotted("set metric"),
		r(`set (?P<k>community|large-community) .+`).slotted("set $k"),
		r(`set (?P<k>comm-list|large-comm-list) \S+ delete`).slotted("set $k"),
		r(`set as-path prepend [0-9 ]+`).slotted("set as-path prepend"),
		r(`set distance \d+`).slotted("set distance"),
		r(`set weight \d+`).slotted("set weight"),
		r(`call \S+`).slotted("call"),
	}}
	iface = &kind{name: "interface", exit: "exit", rules: []rule{
		r(`ipv6 nd ra-lifetime \d+`).slotted("ipv6 nd ra-lifetime"),
		// bgpd turns router advertisements on for an unnumbered neighbour's link.
		r(`ipv6 nd ra-interval \d+`).ignored(),
		r(`no ipv6 nd suppress-ra`).ignored(),
	}}
	bfdProfile = &kind{name: "profile", exit: "exit", rules: []rule{
		r(`receive-interval \d+`).slotted("receive-interval").hiding("receive-interval 300"),
		r(`transmit-interval \d+`).slotted("transmit-interval").hiding("transmit-interval 300"),
		r(`detect-multiplier \d+`).slotted("detect-multiplier").hiding("detect-multiplier 3"),
	}}
	bfd = &kind{name: "bfd", exit: "exit", rules: []rule{
		r(`profile \S+`).opening(bfdProfile),
	}}
	// vrf holds nothing chalkos writes, but FRR may show a VRF it knows.
	vrf  = &kind{name: "vrf", exit: "exit-vrf"}
	root = &kind{name: "the top level", rules: []rule{
		// The AS number is no slot: FRR cannot change an instance's, so a new one replaces it.
		r(`router bgp \d+( vrf \S+)?`).opening(bgp),
		r(`route-map (?P<m>\S+) (permit|deny) (?P<s>\d+)`).slotted("route-map $m $s").opening(routeMap),
		// FRR may name the VRF of a link in another VRF than the default one.
		r(`interface (?P<i>\S+)( vrf \S+)?`).slotted("interface $i").opening(iface),
		r(`bfd`).opening(bfd),
		r(`vrf \S+`).opening(vrf),
		r(`frr version .+`).ignored(),
		r(`frr defaults \S+`).ignored(),
		r(`service integrated-vtysh-config`).ignored(),
		// FRR shows the system's host name, whatever frr.conf sets; Parse keeps frr.conf's in
		// Config.hostname.
		r(`hostname \S+`).ignored(),
		r(`log syslog \S+`).slotted("log syslog"),
		// A prefix-list keys its entries by sequence number.
		r(`(?P<l>(ip|ipv6) prefix-list \S+ seq \d+) (permit|deny) \S+( ge \d+)?( le \d+)?`).slotted("$l"),
		r(`bgp (?P<list>(community-list|large-community-list) standard \S+) seq \d+ (?P<value>(permit|deny) .+)`).keyedByValue(),
		r(`bgp (?P<list>as-path access-list \S+) seq \d+ (?P<value>(permit|deny) .+)`).keyedByValue(),
	}}
)

// A Line is a command of a context.
type Line struct {
	Text string
	rule *rule
	// slot is what the command sets, neighbour the neighbour or peer group it configures, and
	// group the peer group a member's line puts it in. list and value are a list entry's that
	// FRR keys by value.
	slot, neighbour, group, list, value string
}

// A Context is a context of FRR's config, such as a route-map entry or a BGP instance's address
// family, and its commands. The top level is the context without keys.
type Context struct {
	// Keys are the lines that enter the context, from the top level down.
	Keys  []string
	Lines []Line
	kind  *kind
	// id identifies the context across configs: its keys by slot.
	id string
}

// Config is a config's contexts in its order, the top level first.
type Config struct {
	Contexts []*Context
	byID     map[string]*Context
	// hostname is the config's hostname line, which the comparison leaves out.
	hostname string
}

func (c *Config) context(keys, ids []string, k *kind) *Context {
	id := strings.Join(ids, "\n")
	if ctx, ok := c.byID[id]; ok {
		return ctx
	}
	ctx := &Context{Keys: keys, kind: k, id: id}
	c.Contexts = append(c.Contexts, ctx)
	c.byID[id] = ctx
	return ctx
}

// UnknownError is a line the package knows no command of.
type UnknownError struct {
	Line    int
	Text    string
	Context string
}

func (e *UnknownError) Error() string {
	return fmt.Sprintf("line %d: %q in %s is no command chalkos writes", e.Line, e.Text, e.Context)
}

// match is the rule of the kind that matches line, and the indices of its groups.
func (k *kind) match(line string) (*rule, []int) {
	for i := range k.rules {
		if m := k.rules[i].re.FindStringSubmatchIndex(line); m != nil {
			return &k.rules[i], m
		}
	}
	return nil, nil
}

// line is the Line of a match m of the rule.
func (r *rule) line(text string, m []int) Line {
	group := func(name string) string {
		if i := r.re.SubexpIndex(name); i >= 0 && m[2*i] >= 0 {
			return text[m[2*i]:m[2*i+1]]
		}
		return ""
	}
	l := Line{Text: text, rule: r, slot: text, neighbour: group("n"), group: group("g"), list: group("list"), value: group("value")}
	if r.slot != "" {
		l.slot = string(r.re.ExpandString(nil, r.slot, text, m))
	}
	return l
}

// Parse reads a config: frr.conf, or the running config vtysh shows. Comments, blank lines and the
// header of show running-config go; each line must be a command chalkos writes, or one FRR shows
// for them, in its context, or Parse returns an *UnknownError. Lines FRR leaves out as defaults
// go too, so that the two configs compare.
func Parse(text string) (*Config, error) {
	c := &Config{byID: map[string]*Context{}}
	type level struct {
		ctx *Context
		ids []string
	}
	stack := []level{{ctx: c.context(nil, nil, root)}}
	for n, raw := range strings.Split(text, "\n") {
		line := strings.Join(strings.Fields(raw), " ")
		top := stack[len(stack)-1]
		switch {
		case line == "" || strings.HasPrefix(line, "!") || line == "Building configuration..." || line == "Current configuration:":
			continue
		case line == "end":
			stack = stack[:1]
			continue
		case len(stack) > 1 && line == top.ctx.kind.exit:
			stack = stack[:len(stack)-1]
			continue
		}
		rl, m := top.ctx.kind.match(line)
		switch {
		case rl == nil:
			return nil, &UnknownError{Line: n + 1, Text: line, Context: top.ctx.describe()}
		case strings.HasPrefix(line, "hostname ") && top.ctx.kind == root:
			c.hostname = line
		case rl.opens != nil:
			ids := append(slices.Clone(top.ids), rl.line(line, m).slot)
			keys := append(slices.Clone(top.ctx.Keys), line)
			stack = append(stack, level{ctx: c.context(keys, ids, rl.opens), ids: ids})
		case !rl.ignore && line != rl.hidden:
			top.ctx.Lines = append(top.ctx.Lines, rl.line(line, m))
		}
	}
	return c, nil
}

func (ctx *Context) describe() string {
	if len(ctx.Keys) == 0 {
		return ctx.kind.name
	}
	return strings.Join(ctx.Keys, " > ")
}

func (ctx *Context) has(text string) bool {
	return slices.ContainsFunc(ctx.Lines, func(l Line) bool { return l.Text == text })
}

func (ctx *Context) hasSlot(slot string) bool {
	return slices.ContainsFunc(ctx.Lines, func(l Line) bool { return l.slot == slot })
}
