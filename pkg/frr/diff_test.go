package frr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goldens are the frr.conf files the renderers' tests and the image tests hold, by name.
func goldens(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{"router-tracked": "../../cmd/chalkos-render/testdata/router/tracked.frr"}
	for prefix, pattern := range map[string]string{
		"router-":     "../../modules/node/router/tests/*/frr/frr.conf",
		"kubernetes-": "../../modules/node/kubernetes/bgp/tests/*/bgp/frr.conf",
	} {
		paths, err := filepath.Glob(pattern)
		if err != nil || len(paths) == 0 {
			t.Fatalf("no goldens %s: %v", pattern, err)
		}
		for _, p := range paths {
			files[prefix+filepath.Base(filepath.Dir(filepath.Dir(p)))] = p
		}
	}
	texts := map[string]string{}
	for name, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		texts[name] = string(b)
	}
	return texts
}

func mustParse(t *testing.T, text string) *Config {
	t.Helper()
	c, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// canonical is a config as sets: each context's key path with its lines sorted, and the contexts
// FRR shows without lines. Empty interfaces, VRFs and the bfd node are as good as none.
func canonical(c *Config) []string {
	var out []string
	for _, ctx := range c.Contexts {
		path := strings.Join(ctx.Keys, " > ")
		switch {
		case len(ctx.Lines) > 0:
			for _, l := range ctx.Lines {
				out = append(out, path+" : "+l.Text)
			}
		case ctx.kind == routeMap || ctx.kind == bfdProfile || ctx.kind == bgp || ctx.kind == addressFamily:
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

// simulate runs the plan on c as FRR would: no deletes a command, a context, or a neighbour with
// its config; a command replaces the one of its slot; a community-list or as-path access-list
// refuses a value it holds and deletes an entry by value. It is a regression model that shares
// the implementation's rules, so it cannot catch a rule that misdescribes FRR; the frr-reload
// check, which reloads a real FRR, is the oracle for those.
func simulate(t *testing.T, c *Config, p Plan) {
	t.Helper()
	find := func(keys []string) *Context {
		for _, ctx := range c.Contexts {
			if slices.Equal(ctx.Keys, keys) {
				return ctx
			}
		}
		return nil
	}
	enter := func(keys []string) *Context {
		ids := []string{}
		k := root
		ctx := c.byID[""]
		for i, key := range keys {
			o, m := k.match(key)
			if o == nil || o.opens == nil {
				t.Fatalf("%q opens no context in %s", key, k.name)
			}
			ids = append(ids, o.line(key, m).slot)
			k = o.opens
			ctx = c.context(slices.Clone(keys[:i+1]), slices.Clone(ids), k)
			ctx.Keys = slices.Clone(keys[:i+1])
		}
		return ctx
	}
	forget := func(instance, n string) {
		for _, ctx := range c.Contexts {
			if len(ctx.Keys) > 0 && ctx.Keys[0] == instance {
				ctx.Lines = slices.DeleteFunc(ctx.Lines, func(l Line) bool { return l.neighbour == n })
			}
		}
	}
	for _, o := range slices.Concat(p.Remove, p.Add, p.Drop) {
		if o.Cmd == "" {
			enter(o.Keys)
			continue
		}
		if cmd, ok := strings.CutPrefix(o.Cmd, "no "); ok && find(append(slices.Clone(o.Keys), cmd)) != nil {
			whole := append(slices.Clone(o.Keys), cmd)
			c.Contexts = slices.DeleteFunc(c.Contexts, func(ctx *Context) bool {
				if len(ctx.Keys) >= len(whole) && slices.Equal(ctx.Keys[:len(whole)], whole) {
					delete(c.byID, ctx.id)
					return true
				}
				return false
			})
			continue
		}
		ctx := enter(o.Keys)
		text := o.Cmd
		del := false
		if cmd, ok := strings.CutPrefix(o.Cmd, "no "); ok {
			if r, _ := ctx.kind.match(o.Cmd); r == nil {
				text, del = cmd, true
			}
		}
		rl, m := ctx.kind.match(text)
		if rl == nil || rl.opens != nil {
			t.Fatalf("%q is no command of %s", text, ctx.describe())
		}
		if rl.ignore {
			// The hostname each plan sets.
			continue
		}
		l := rl.line(text, m)
		n := l.neighbour
		// FRR keys a community-list's or as-path access-list's entries by value.
		sameValue := func(x Line) bool { return x.rule.byValue && x.list == l.list && x.value == l.value }
		if del {
			if rl.byValue {
				if !slices.ContainsFunc(ctx.Lines, sameValue) {
					t.Fatalf("deletes %q, whose value FRR's list lacks", text)
				}
				ctx.Lines = slices.DeleteFunc(ctx.Lines, sameValue)
				continue
			}
			if !ctx.has(text) {
				t.Fatalf("%s deletes %q, which FRR does not run", ctx.describe(), text)
			}
			switch rl.defines {
			case definesGroup:
				var members []string
				for _, l := range ctx.Lines {
					if l.rule.defines == definesMember && l.group == n {
						members = append(members, l.neighbour)
					}
				}
				for _, m := range append(members, n) {
					forget(ctx.Keys[0], m)
				}
			case definesMember:
				forget(ctx.Keys[0], n)
			}
			ctx.Lines = slices.DeleteFunc(ctx.Lines, func(l Line) bool { return l.Text == text })
			continue
		}
		if rl.byValue && slices.ContainsFunc(ctx.Lines, sameValue) {
			// FRR refuses a value its list holds at any sequence number.
			continue
		}
		if n != "" && rl.defines == definesNothing && !slices.ContainsFunc(c.byID[instanceID(ctx.id)].Lines, func(l Line) bool {
			return l.rule.defines != definesNothing && l.neighbour == n
		}) {
			t.Fatalf("%s configures %s before it exists", ctx.describe(), n)
		}
		ctx.Lines = slices.DeleteFunc(ctx.Lines, func(x Line) bool { return x.slot == l.slot })
		ctx.Lines = append(ctx.Lines, l)
	}
}

func TestGoldensParseAndHoldNoChange(t *testing.T) {
	for name, text := range goldens(t) {
		c := mustParse(t, text)
		if p := Diff(c, mustParse(t, text)); !p.Empty() {
			t.Errorf("%s: a config differs from itself by:\n%s", name, p)
		}
	}
}

// Each golden turns into each other one, deleting what the target lacks.
func TestGoldenTransitions(t *testing.T) {
	texts := goldens(t)
	for from, a := range texts {
		for to, b := range texts {
			if from == to {
				continue
			}
			t.Run(from+"/"+to, func(t *testing.T) {
				running, target := mustParse(t, a), mustParse(t, b)
				p := Diff(running, target)
				simulate(t, running, p)
				if got, want := canonical(running), canonical(target); !slices.Equal(got, want) {
					t.Errorf("the plan\n%s\nleaves\n%s\nnot\n%s", p, strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				if again := Diff(running, target); !again.Empty() {
					t.Errorf("a second plan follows:\n%s", again)
				}
			})
		}
	}
}

const base = `frr defaults traditional
hostname r1
log syslog informational
service integrated-vtysh-config
ip prefix-list pl-a seq 5 permit 192.0.2.0/24
bgp community-list standard cl-a seq 5 permit 65000:1
bgp community-list standard cl-a seq 10 permit 65000:2
bgp community-list standard cl-a seq 15 permit 65000:3
route-map rp-a permit 10
 match ip address prefix-list pl-a
exit
route-map rp-a deny 65535
exit
interface eth1
 ipv6 nd ra-lifetime 0
exit
router bgp 64512
 bgp router-id 192.0.2.1
 no bgp default ipv4-unicast
 neighbor peer-a peer-group
 neighbor peer-a remote-as 65001
 neighbor peer-a password one
 neighbor peer-a bfd
 neighbor peer-a bfd profile chalkos
 neighbor 192.0.2.2 peer-group peer-a
 neighbor peer-b peer-group
 neighbor peer-b remote-as external
 neighbor eth1 interface peer-group peer-b
 neighbor fd00::1 peer-group peer-b
 neighbor fd00::1 interface eth1
 !
 address-family ipv4 unicast
  network 192.0.2.0/24 route-map rp-a
  redistribute connected route-map rp-a
  maximum-paths 64
  maximum-paths ibgp 1
  neighbor peer-a activate
  neighbor peer-a default-originate route-map rp-a
  neighbor peer-a route-map rp-a in
 exit-address-family
 !
 address-family ipv6 unicast
  neighbor peer-b activate
 exit-address-family
exit
bfd
 profile chalkos
  receive-interval 100
 exit
exit
end
`

// edit is base with each pair of old and new lines replaced; an empty new line deletes the old.
func edit(t *testing.T, pairs ...string) string {
	t.Helper()
	lines := strings.Split(base, "\n")
	for i := 0; i < len(pairs); i += 2 {
		j := slices.Index(lines, pairs[i])
		if j < 0 {
			t.Fatalf("base has no line %q", pairs[i])
		}
		if pairs[i+1] == "" {
			lines = slices.Delete(lines, j, j+1)
		} else {
			lines[j] = pairs[i+1]
		}
	}
	return strings.Join(lines, "\n")
}

func TestPlans(t *testing.T) {
	// Every plan that changes something sets base's hostname first among its adds.
	const host = "hostname r1\n"
	for _, tc := range []struct {
		name   string
		target string
		want   string
		// prefix compares the start of the plan alone, when a whole instance follows.
		prefix bool
	}{{
		name: "a removed peer group takes its members and config",
		target: edit(t,
			" neighbor peer-a peer-group", "",
			" neighbor peer-a remote-as 65001", "",
			" neighbor peer-a password one", "",
			" neighbor peer-a bfd", "",
			" neighbor peer-a bfd profile chalkos", "",
			" neighbor 192.0.2.2 peer-group peer-a", "",
			"  neighbor peer-a activate", "",
			"  neighbor peer-a default-originate route-map rp-a", "",
			"  neighbor peer-a route-map rp-a in", ""),
		want: "router bgp 64512\n no neighbor peer-a peer-group\nexit\n" + host,
	}, {
		name:   "a removed member goes alone",
		target: edit(t, " neighbor eth1 interface peer-group peer-b", ""),
		want:   "router bgp 64512\n no neighbor eth1 interface peer-group peer-b\nexit\n" + host,
	}, {
		name:   "a member that moves comes back with its config",
		target: edit(t, " neighbor fd00::1 peer-group peer-b", " neighbor fd00::1 peer-group peer-a"),
		want:   "router bgp 64512\n no neighbor fd00::1 peer-group peer-b\nexit\n" + host + "router bgp 64512\n neighbor fd00::1 peer-group peer-a\n neighbor fd00::1 interface eth1\nexit\n",
	}, {
		name:   "a new password and route-map replace the old without a deletion",
		target: edit(t, " neighbor peer-a password one", " neighbor peer-a password two", "  neighbor peer-a route-map rp-a in", "  neighbor peer-a route-map rp-b in"),
		want:   host + "router bgp 64512\n neighbor peer-a password two\n address-family ipv4 unicast\n  neighbor peer-a route-map rp-b in\n exit-address-family\nexit\n",
	}, {
		name: "a renamed route-map of a network, a redistribution and a default route replaces the old",
		target: edit(t,
			"  network 192.0.2.0/24 route-map rp-a", "  network 192.0.2.0/24 route-map rp-b",
			"  redistribute connected route-map rp-a", "  redistribute connected route-map rp-b",
			"  neighbor peer-a default-originate route-map rp-a", "  neighbor peer-a default-originate route-map rp-b"),
		want: host + "router bgp 64512\n address-family ipv4 unicast\n  network 192.0.2.0/24 route-map rp-b\n  redistribute connected route-map rp-b\n" +
			"  neighbor peer-a default-originate route-map rp-b\n exit-address-family\nexit\n",
	}, {
		name:   "a network that loses its route-map is a new command",
		target: edit(t, "  network 192.0.2.0/24 route-map rp-a", "  network 192.0.2.0/24"),
		want: "router bgp 64512\n address-family ipv4 unicast\n  no network 192.0.2.0/24 route-map rp-a\n exit-address-family\nexit\n" +
			host + "router bgp 64512\n address-family ipv4 unicast\n  network 192.0.2.0/24\n exit-address-family\nexit\n",
	}, {
		name:   "BFD off deletes the profile before bfd",
		target: edit(t, " neighbor peer-a bfd", "", " neighbor peer-a bfd profile chalkos", ""),
		want:   "router bgp 64512\n no neighbor peer-a bfd profile chalkos\n no neighbor peer-a bfd\nexit\n" + host,
	}, {
		name:   "a removed route-map entry goes after what names it",
		target: edit(t, "route-map rp-a deny 65535", "", "exit", "", "  neighbor peer-a route-map rp-a in", ""),
		want: "router bgp 64512\n address-family ipv4 unicast\n  no neighbor peer-a route-map rp-a in\n exit-address-family\nexit\n" +
			host + "no route-map rp-a deny 65535\n",
	}, {
		name:   "a route-map entry that changes its action is entered with the new one",
		target: edit(t, "route-map rp-a deny 65535", "route-map rp-a permit 65535"),
		want:   host + "route-map rp-a permit 65535\nexit\n",
	}, {
		name:   "a prefix-list entry is replaced by its sequence number",
		target: edit(t, "ip prefix-list pl-a seq 5 permit 192.0.2.0/24", "ip prefix-list pl-a seq 5 deny 192.0.2.0/24"),
		want:   host + "ip prefix-list pl-a seq 5 deny 192.0.2.0/24\n",
	}, {
		name: "a community-list's entries that move go before the adds, as FRR keys them by value",
		target: edit(t,
			"bgp community-list standard cl-a seq 5 permit 65000:1", "bgp community-list standard cl-a seq 5 permit 65000:2",
			"bgp community-list standard cl-a seq 10 permit 65000:2", "bgp community-list standard cl-a seq 10 permit 65000:3",
			"bgp community-list standard cl-a seq 15 permit 65000:3", ""),
		want: "no bgp community-list standard cl-a seq 15 permit 65000:3\nno bgp community-list standard cl-a seq 10 permit 65000:2\n" +
			"no bgp community-list standard cl-a seq 5 permit 65000:1\n" + host +
			"bgp community-list standard cl-a seq 5 permit 65000:2\nbgp community-list standard cl-a seq 10 permit 65000:3\n",
	}, {
		name:   "a community-list's entry that stays at its sequence number stays",
		target: edit(t, "bgp community-list standard cl-a seq 15 permit 65000:3", "bgp community-list standard cl-a seq 15 permit 65000:4"),
		want:   "no bgp community-list standard cl-a seq 15 permit 65000:3\n" + host + "bgp community-list standard cl-a seq 15 permit 65000:4\n",
	}, {
		name: "a removed community-list goes last",
		target: edit(t,
			"bgp community-list standard cl-a seq 5 permit 65000:1", "",
			"bgp community-list standard cl-a seq 10 permit 65000:2", "",
			"bgp community-list standard cl-a seq 15 permit 65000:3", ""),
		want: host + "no bgp community-list standard cl-a seq 15 permit 65000:3\nno bgp community-list standard cl-a seq 10 permit 65000:2\n" +
			"no bgp community-list standard cl-a seq 5 permit 65000:1\n",
	}, {
		name:   "a removed address family's commands go one by one",
		target: edit(t, " address-family ipv6 unicast", "", "  neighbor peer-b activate", "", " exit-address-family", ""),
		want:   "router bgp 64512\n address-family ipv6 unicast\n  no neighbor peer-b activate\n exit-address-family\nexit\n" + host,
	}, {
		name:   "a default maximum-paths is FRR's own",
		target: edit(t, "  maximum-paths 64", "", "  maximum-paths ibgp 1", "  maximum-paths ibgp 64"),
		want:   "router bgp 64512\n address-family ipv4 unicast\n  no maximum-paths ibgp 1\n exit-address-family\nexit\n" + host,
	}, {
		name:   "a removed interface keeps its node",
		target: edit(t, " ipv6 nd ra-lifetime 0", ""),
		want:   "interface eth1\n no ipv6 nd ra-lifetime 0\nexit\n" + host,
	}, {
		name:   "the default BFD timers delete the profile's",
		target: edit(t, "  receive-interval 100", "  receive-interval 300"),
		want:   "bfd\n profile chalkos\n  no receive-interval 100\n exit\nexit\n" + host,
	}, {
		name:   "a new AS number replaces the instance",
		target: edit(t, "router bgp 64512", "router bgp 64513"),
		want:   "no router bgp 64512\n" + host + "router bgp 64513\n bgp router-id 192.0.2.1\n",
		prefix: true,
	}, {
		name:   "a new hostname alone plans nothing",
		target: edit(t, "hostname r1", "hostname r2"),
	}, {
		name:   "a plan sets the target's hostname",
		target: edit(t, "hostname r1", "hostname r2", "ip prefix-list pl-a seq 5 permit 192.0.2.0/24", "ip prefix-list pl-a seq 5 deny 192.0.2.0/24"),
		want:   "hostname r2\nip prefix-list pl-a seq 5 deny 192.0.2.0/24\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			running, target := mustParse(t, base), mustParse(t, tc.target)
			p := Diff(running, target)
			got := p.Script()
			if tc.prefix && len(got) > len(tc.want) {
				got = got[:len(tc.want)]
			}
			if got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
			simulate(t, running, p)
			if again := Diff(running, target); !again.Empty() {
				t.Errorf("a second plan follows:\n%s", again)
			}
		})
	}
}

// A list whose entries shift down one sequence number: deleting 10:B after adding 5:B
// would delete the new entry by its value, and FRR would refuse 5:B while 10:B stood.
func TestCommunityListShift(t *testing.T) {
	list := func(entries ...string) string {
		var b strings.Builder
		for i, e := range entries {
			fmt.Fprintf(&b, "bgp community-list standard cl seq %d permit %s\n", 5*(i+1), e)
		}
		return b.String()
	}
	running, target := mustParse(t, list("65000:1", "65000:2", "65000:3")), mustParse(t, list("65000:2", "65000:3"))
	simulate(t, running, Diff(running, target))
	if got, want := canonical(running), canonical(target); !slices.Equal(got, want) {
		t.Errorf("the list holds\n%s\nnot\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseRefusesUnknownCommands(t *testing.T) {
	for _, text := range []string{
		"router bgp 1\n neighbor x ebgp-multihop 2\nexit\n",
		"router ospf\nexit\n",
		"route-map x permit 10\n set tag 5\nexit\n",
		"ip route 0.0.0.0/0 192.0.2.1\n",
	} {
		_, err := Parse(text)
		if _, ok := errors.AsType[*UnknownError](err); !ok {
			t.Errorf("Parse(%q) = %v, want an *UnknownError", text, err)
		}
	}
}

func TestStringHidesPasswords(t *testing.T) {
	p := Diff(mustParse(t, base), mustParse(t, edit(t, " neighbor peer-a password one", " neighbor peer-a password secret")))
	if s := p.String(); strings.Contains(s, "secret") || !strings.Contains(s, "password <hidden>") {
		t.Errorf("String() = %q", s)
	}
}
