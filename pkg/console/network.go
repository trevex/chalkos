package console

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// networkEvery is the time between two reads of the Network tab's diagnostics while it is on
// screen.
const networkEvery = 5 * time.Second

// network is the Network tab: each link's state, MAC address, MTU and addresses, the default
// routes, and the DNS servers.
type network struct {
	o       Options
	c       Client
	links   Source[[]ipLink]
	routes4 Source[[]ipRoute]
	routes6 Source[[]ipRoute]
	dns     Source[[]string]
	reading bool
	read    time.Time
	pane    Pane
}

func newNetwork(o Options) *network { return &network{o: o} }

func (n *network) Title() string                     { return "Network" }
func (n *network) Applies(*nodev1.InfoResponse) bool { return true }
func (n *network) Init(c Client) tea.Cmd             { n.c = c; return nil }

// networkMsg is what one read of the Network tab gave.
type networkMsg struct {
	links            []ipLink
	linksErr         error
	routes4, routes6 []ipRoute
	routes4Err       error
	routes6Err       error
	dns              []string
	dnsErr           error
	at               time.Time
}

func (n *network) Update(msg tea.Msg) (Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case TickMsg:
		if msg.Shown && !msg.Idle && !n.reading && msg.Now.Sub(n.read) >= networkEvery {
			n.reading = true
			return n, n.readAll(msg.Now)
		}
	case networkMsg:
		n.links.Set(msg.links, msg.linksErr, msg.at)
		n.routes4.Set(msg.routes4, msg.routes4Err, msg.at)
		n.routes6.Set(msg.routes6, msg.routes6Err, msg.at)
		n.dns.Set(msg.dns, msg.dnsErr, msg.at)
		n.reading, n.read = false, msg.at
	case tea.KeyMsg:
		n.pane.Key(msg)
	}
	return n, nil
}

// readAll runs the diagnostics one after the other, as chalkd runs few at a time and the header
// may run one too, and reads the DNS servers.
func (n *network) readAll(now time.Time) tea.Cmd {
	c, dnsServers := n.c, n.o.DNSServers
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		r := networkMsg{at: now}
		var out []byte
		if out, r.linksErr = c.Diagnostic(ctx, "addresses", nil); r.linksErr == nil {
			r.links, r.linksErr = parseLinks(out)
		}
		if out, r.routes4Err = c.Diagnostic(ctx, "routes", map[string]string{"family": "inet", "table": "main"}); r.routes4Err == nil {
			r.routes4, r.routes4Err = parseRoutes(out)
		}
		if out, r.routes6Err = c.Diagnostic(ctx, "routes", map[string]string{"family": "inet6", "table": "main"}); r.routes6Err == nil {
			r.routes6, r.routes6Err = parseRoutes(out)
		}
		r.dns, r.dnsErr = dnsServers()
		return r
	}
}

func (n *network) View(width, height int) string {
	if n.read.IsZero() {
		return "Reading the network..."
	}
	var b strings.Builder
	if p := n.links.Problem("addresses"); p != "" {
		b.WriteString(ErrorStyle.Render(p) + "\n")
	}
	if n.links.Value != nil {
		t := statusview.Table(&b)
		fmt.Fprintln(t, "LINK\tSTATE\tMAC\tMTU\tADDRESSES")
		for _, l := range n.links.Value {
			if slices.Contains(l.Flags, "LOOPBACK") {
				continue
			}
			first := ""
			if len(l.AddrInfo) > 0 {
				first = addressWithPrefix(l.AddrInfo[0])
			}
			fmt.Fprintf(t, "%s\t%s\t%s\t%d\t%s\n", oneLine(l.IfName), oneLine(l.OperState), oneLine(l.Address), l.MTU, first)
			for _, a := range l.AddrInfo[min(1, len(l.AddrInfo)):] {
				fmt.Fprintf(t, "\t\t\t\t%s\n", addressWithPrefix(a))
			}
		}
		t.Flush()
	}
	b.WriteString("default routes:\n")
	var routes []string
	for _, src := range []struct {
		what   string
		routes Source[[]ipRoute]
	}{{"IPv4 routes", n.routes4}, {"IPv6 routes", n.routes6}} {
		if p := src.routes.Problem(src.what); p != "" {
			routes = append(routes, ErrorStyle.Render(p))
		}
		for _, r := range src.routes.Value {
			if r.Dst == "default" {
				routes = append(routes, defaultRoutes(r)...)
			}
		}
	}
	if len(routes) == 0 {
		routes = []string{"none"}
	}
	for _, r := range routes {
		b.WriteString("  " + r + "\n")
	}
	if p := n.dns.Problem("DNS servers"); p != "" {
		b.WriteString(ErrorStyle.Render(p) + "\n")
	}
	if n.dns.Value != nil || n.dns.Err == nil {
		b.WriteString("DNS servers: " + oneLine(cmp.Or(strings.Join(n.dns.Value, ", "), "none")) + "\n")
	}
	return n.pane.View(b.String(), width, height)
}

// addressWithPrefix is an address with the length of its prefix, such as 192.168.1.50/24.
func addressWithPrefix(a ipAddr) string { return oneLine(fmt.Sprintf("%s/%d", a.Local, a.PrefixLen)) }

// defaultRoutes are the lines of a default route: its gateway, link and protocol, one line for
// each path of a multipath route.
func defaultRoutes(r ipRoute) []string {
	proto := ""
	if r.Protocol != "" {
		proto = " (" + r.Protocol + ")"
	}
	if len(r.Nexthops) == 0 {
		return []string{oneLine(via(r.Gateway, r.Dev) + proto)}
	}
	lines := make([]string, len(r.Nexthops))
	for i, h := range r.Nexthops {
		lines[i] = oneLine(fmt.Sprintf("%s weight %d%s", via(h.Gateway, h.Dev), h.Weight, proto))
	}
	return lines
}

// via names a route's next hop: its gateway, if any, and its link.
func via(gateway, dev string) string {
	if gateway == "" {
		return "dev " + dev
	}
	return "via " + gateway + " dev " + dev
}
