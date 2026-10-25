package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The documents of the router layer's kinds, as the outputs router-sysctl and router-nftables read
// them: with the defaults of their schemas, each kind's sorted by name.

type routerSpec struct {
	Forwarding struct {
		IPv4 bool `json:"ipv4"`
		IPv6 bool `json:"ipv6"`
	} `json:"forwarding"`
	RPFilter     string `json:"rpFilter"`
	ECMPHash     string `json:"ecmpHash"`
	ConntrackMax *int64 `json:"conntrackMax"`
	Management   struct {
		// Interfaces is nil for every interface.
		Interfaces []string `json:"interfaces"`
	} `json:"management"`
}

// defaultRouter is the Router of a node whose config holds none: the kind's defaults.
func defaultRouter() routerSpec {
	var r routerSpec
	r.Forwarding.IPv4, r.Forwarding.IPv6 = true, true
	r.RPFilter, r.ECMPHash = "loose", "l4"
	return r
}

type zoneSpec struct {
	Interfaces []string `json:"interfaces"`
}

type policySpec struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Default string         `json:"default"`
	Rules   []firewallRule `json:"rules"`
}

type firewallRule struct {
	Family           *string     `json:"family"`
	Protocol         *string     `json:"protocol"`
	SourcePorts      []portRange `json:"sourcePorts"`
	DestinationPorts []portRange `json:"destinationPorts"`
	Source           []string    `json:"source"`
	Destination      []string    `json:"destination"`
	ICMPTypes        []string    `json:"icmpTypes"`
	Action           string      `json:"action"`
	Log              bool        `json:"log"`
}

type addressSetSpec struct {
	Family   string   `json:"family"`
	Prefixes []string `json:"prefixes"`
}

type natSpec struct {
	Type   string    `json:"type"`
	Family string    `json:"family"`
	Out    *natPlace `json:"out"`
	In     *natPlace `json:"in"`
	Match  struct {
		Source           []string    `json:"source"`
		Destination      []string    `json:"destination"`
		Protocol         *string     `json:"protocol"`
		DestinationPorts []portRange `json:"destinationPorts"`
	} `json:"match"`
	To *struct {
		Address string `json:"address"`
		Port    *int   `json:"port"`
	} `json:"to"`
}

type natPlace struct {
	Zone      *string `json:"zone"`
	Interface *string `json:"interface"`
}

// portRange is a port from 1, or a range of ports "<first>-<last>", as nftables writes both.
type portRange string

func (p *portRange) UnmarshalJSON(data []byte) error {
	var n uint16
	if err := json.Unmarshal(data, &n); err == nil && n > 0 {
		*p = portRange(strconv.Itoa(int(n)))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("a port is a number from 1 to 65535 or a range \"<first>-<last>\", not %s", data)
	}
	first, last, ok := strings.Cut(s, "-")
	a, errA := strconv.ParseUint(first, 10, 16)
	b, errB := strconv.ParseUint(last, 10, 16)
	if !ok || errA != nil || errB != nil || a == 0 || a > b {
		return fmt.Errorf("%q is no range of ports \"<first>-<last>\" from 1 to 65535 that ends no lower than it starts", s)
	}
	*p = portRange(fmt.Sprintf("%d-%d", a, b))
	return nil
}

// named is the spec of a document, with the document's name.
type named[T any] struct {
	Name string
	Spec T
}

// routerDocuments are the router kinds' documents of an output's input.
type routerDocuments struct {
	Routers  []named[routerSpec]
	Zones    []named[zoneSpec]
	Policies []named[policySpec]
	Sets     []named[addressSetSpec]
	NAT      []named[natSpec]
}

// readRouterDocuments decodes the router kinds' documents of in; a kind the output does not read
// has none.
func readRouterDocuments(in map[string]any) (routerDocuments, error) {
	byKind, _ := in["documents"].(map[string]any)
	var d routerDocuments
	var err error
	if d.Routers, err = decodeKind[routerSpec](byKind, "Router"); err != nil {
		return d, err
	}
	if d.Zones, err = decodeKind[zoneSpec](byKind, "FirewallZone"); err != nil {
		return d, err
	}
	if d.Policies, err = decodeKind[policySpec](byKind, "FirewallPolicy"); err != nil {
		return d, err
	}
	if d.Sets, err = decodeKind[addressSetSpec](byKind, "AddressSet"); err != nil {
		return d, err
	}
	d.NAT, err = decodeKind[natSpec](byKind, "NATRule")
	return d, err
}

// decodeKind decodes the specs of the documents of kind in byKind, in their order.
func decodeKind[T any](byKind map[string]any, kind string) ([]named[T], error) {
	docs, _ := byKind[kind].([]any)
	out := make([]named[T], 0, len(docs))
	for _, d := range docs {
		obj, _ := d.(map[string]any)
		name, err := documentName(kind, obj)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(obj["spec"])
		if err != nil {
			return nil, err
		}
		var spec T
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, name, err)
		}
		out = append(out, named[T]{name, spec})
	}
	return out, nil
}

// router is the node's Router, or the kind's defaults when its config holds none.
func (d routerDocuments) router() routerSpec {
	if len(d.Routers) == 0 {
		return defaultRouter()
	}
	return d.Routers[0].Spec
}
