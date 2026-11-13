package console

import (
	"encoding/json"
	"fmt"
)

// ipLink is a link as ip -json address show prints it.
type ipLink struct {
	IfName    string   `json:"ifname"`
	OperState string   `json:"operstate"`
	Address   string   `json:"address"`
	MTU       int      `json:"mtu"`
	Flags     []string `json:"flags"`
	AddrInfo  []ipAddr `json:"addr_info"`
}

// ipAddr is an address of a link.
type ipAddr struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	Scope     string `json:"scope"`
	PrefixLen int    `json:"prefixlen"`
}

// ipRoute is a route as ip -json route show prints it.
type ipRoute struct {
	Dst      string `json:"dst"`
	Gateway  string `json:"gateway"`
	Dev      string `json:"dev"`
	Protocol string `json:"protocol"`
	Metric   int    `json:"metric"`
	// Nexthops are the paths of a multipath route, which has no gateway or dev of its own.
	Nexthops []ipNexthop `json:"nexthops"`
}

// ipNexthop is one path of a multipath route.
type ipNexthop struct {
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	Weight  int    `json:"weight"`
}

// parseLinks reads the output of the addresses diagnostic.
func parseLinks(out []byte) ([]ipLink, error) {
	var links []ipLink
	if err := json.Unmarshal(out, &links); err != nil {
		return nil, fmt.Errorf("read ip's addresses: %w", err)
	}
	return links, nil
}

// parseRoutes reads the output of the routes diagnostic.
func parseRoutes(out []byte) ([]ipRoute, error) {
	var routes []ipRoute
	if err := json.Unmarshal(out, &routes); err != nil {
		return nil, fmt.Errorf("read ip's routes: %w", err)
	}
	return routes, nil
}

// globalAddresses lists the addresses of the addresses diagnostic's output whose scope is neither
// link nor host, in the order ip prints them.
func globalAddresses(out []byte) ([]string, error) {
	links, err := parseLinks(out)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, l := range links {
		for _, a := range l.AddrInfo {
			if a.Scope != "link" && a.Scope != "host" {
				addrs = append(addrs, a.Local)
			}
		}
	}
	return addrs, nil
}
