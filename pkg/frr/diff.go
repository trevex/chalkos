package frr

import (
	"regexp"
	"slices"
	"strings"
)

// An Op is a command and the contexts vtysh enters to run it.
type Op struct {
	Keys []string
	// Cmd is the command; empty, the op enters the contexts alone, which creates them.
	Cmd   string
	exits []string
}

// A Plan is what turns FRR's running config into frr.conf, in the order vtysh runs it.
type Plan struct {
	// Remove deletes commands frr.conf lacks from contexts, then the neighbours and peer groups
	// it lacks, then the BGP instances it lacks or has with another AS number.
	Remove []Op
	// Add creates the contexts and commands FRR lacks, in frr.conf's order, which defines what
	// a command names before the command.
	Add []Op
	// Drop deletes the route-map entries, BFD profiles and lists frr.conf lacks, once nothing
	// names them.
	Drop []Op
}

// Empty reports whether FRR runs frr.conf already.
func (p Plan) Empty() bool { return len(p.Remove)+len(p.Add)+len(p.Drop) == 0 }

// Script is the plan as a file vtysh -f runs: each op in its contexts, which ops in a row share.
func (p Plan) Script() string {
	var b strings.Builder
	var open Op
	leave := func(depth int) {
		for i := len(open.Keys) - 1; i >= depth; i-- {
			b.WriteString(strings.Repeat(" ", i) + open.exits[i] + "\n")
		}
	}
	for _, o := range slices.Concat(p.Remove, p.Add, p.Drop) {
		same := 0
		for same < min(len(open.Keys), len(o.Keys)) && open.Keys[same] == o.Keys[same] {
			same++
		}
		leave(same)
		for i := same; i < len(o.Keys); i++ {
			b.WriteString(strings.Repeat(" ", i) + o.Keys[i] + "\n")
		}
		if o.Cmd != "" {
			b.WriteString(strings.Repeat(" ", len(o.Keys)) + o.Cmd + "\n")
		}
		open = o
	}
	leave(0)
	return b.String()
}

var password = regexp.MustCompile(`( password )\S+`)

// String is the script without the peers' passwords, for the log.
func (p Plan) String() string { return password.ReplaceAllString(p.Script(), "${1}<hidden>") }

func negate(cmd string) string {
	if c, ok := strings.CutPrefix(cmd, "no "); ok {
		return c
	}
	return "no " + cmd
}

func (ctx *Context) op(cmd string) Op { return Op{Keys: ctx.Keys, Cmd: cmd, exits: ctx.exits()} }

// parentOp runs cmd in the context's parent, as no route-map does.
func (ctx *Context) parentOp(cmd string) Op {
	n := len(ctx.Keys) - 1
	return Op{Keys: ctx.Keys[:n], Cmd: cmd, exits: ctx.exits()[:n]}
}

func (ctx *Context) exits() []string {
	exits := make([]string, len(ctx.Keys))
	k := root
	for i, key := range ctx.Keys {
		rl, _ := k.match(key)
		k = rl.opens
		exits[i] = k.exit
	}
	return exits
}

// neighbours are what a BGP instance's deleted neighbours and peer groups take with them.
type neighbours struct {
	// gone are the neighbours and peer groups FRR deletes, with their config: those whose line
	// that creates them frr.conf lacks, and the members of a deleted peer group.
	gone map[string]bool
	// members and groups are the lines that delete them, members first.
	members, groups []Op
}

// deletedNeighbours finds the neighbours of the instance rc that frr.conf's tc lacks.
func deletedNeighbours(rc, tc *Context) neighbours {
	n := neighbours{gone: map[string]bool{}}
	groupOf := map[string]string{}
	for _, l := range rc.Lines {
		if l.rule.defines == "member" {
			groupOf[l.neighbour] = l.group
		}
	}
	for _, l := range rc.Lines {
		if l.rule.defines != "" && !tc.has(l.Text) {
			n.gone[l.neighbour] = true
		}
	}
	for _, l := range rc.Lines {
		switch {
		case l.rule.defines == "group" && n.gone[l.neighbour]:
			n.groups = append(n.groups, rc.op(negate(l.Text)))
		case l.rule.defines == "member" && !tc.has(l.Text) && !n.gone[l.group]:
			n.members = append(n.members, rc.op(negate(l.Text)))
		}
	}
	for m, g := range groupOf {
		if n.gone[g] {
			n.gone[m] = true
		}
	}
	return n
}

// Diff is the plan that turns the running config into the target. A command whose slot the
// target fills with another value is no deletion: the target's replaces it, so that a route-map
// or a password changes without a moment in which none applies.
func Diff(running, target *Config) Plan {
	var p Plan
	var lines, mainLines, members, groups, vrfInstances, instances []Op
	// The neighbours each BGP instance both configs hold deletes, by its key.
	deleted := map[string]neighbours{}
	for _, rc := range running.Contexts {
		if tc := target.byID[rc.id]; rc.kind == bgp && tc != nil {
			n := deletedNeighbours(rc, tc)
			deleted[rc.Keys[0]] = n
			members = append(members, n.members...)
			groups = append(groups, n.groups...)
		}
	}
	for _, rc := range running.Contexts {
		tc := target.byID[rc.id]
		if tc == nil {
			switch rc.kind {
			case bgp:
				if strings.Contains(rc.Keys[0], " vrf ") {
					vrfInstances = append(vrfInstances, rc.parentOp(negate(rc.Keys[0])))
				} else {
					instances = append(instances, rc.parentOp(negate(rc.Keys[0])))
				}
				continue
			case routeMap, bfdProfile:
				p.Drop = append(p.Drop, rc.parentOp(negate(rc.Keys[len(rc.Keys)-1])))
				continue
			case addressFamily:
				// An instance frr.conf lacks goes whole.
				if target.byID[strings.SplitN(rc.id, "\n", 2)[0]] == nil {
					continue
				}
			}
			// Interfaces and VRFs stay, and an address family has no no form: their commands go.
		}
		var gone map[string]bool
		if len(rc.Keys) > 0 {
			gone = deleted[rc.Keys[0]].gone
		}
		for _, l := range slices.Backward(rc.Lines) {
			if tc != nil && (tc.has(l.Text) || l.slot != l.Text && tc.hasSlot(l.slot)) || gone[l.neighbour] {
				continue
			}
			op := rc.op(negate(l.Text))
			switch rc.kind {
			case root:
				p.Drop = append(p.Drop, op)
			case bgp:
				mainLines = append(mainLines, op)
			default:
				lines = append(lines, op)
			}
		}
	}
	p.Remove = slices.Concat(lines, mainLines, members, groups, vrfInstances, instances)

	for _, tc := range target.Contexts {
		rc := running.byID[tc.id]
		var gone map[string]bool
		if len(tc.Keys) > 0 {
			gone = deleted[tc.Keys[0]].gone
		}
		if len(tc.Lines) == 0 && (rc == nil || !slices.Equal(rc.Keys, tc.Keys)) {
			// A new context, or a route-map entry that changes its action.
			p.Add = append(p.Add, tc.op(""))
		}
		for _, l := range tc.Lines {
			// A deleted neighbour that frr.conf keeps comes back without its config.
			if rc == nil || !rc.has(l.Text) || gone[l.neighbour] || !slices.Equal(rc.Keys, tc.Keys) {
				p.Add = append(p.Add, tc.op(l.Text))
			}
		}
	}
	return p
}
