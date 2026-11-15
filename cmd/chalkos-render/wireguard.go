package main

import (
	"maps"
	"net/netip"
	"slices"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// withWireGuardUnits is docs with a NetDev and a Network 00-wg-<name> per WireGuard. The NetDev
// creates the interface with its port, private key, a [WireGuardPeer] per peer and RouteTable=,
// which routes the peers' allowed IPs in its VRF's table; the Network gives the link its addresses,
// VRF and MTU. Keys are networkd credentials @secret_<secret>.<key>, which chalkd loads from the
// Secrets, so the units hold no key. It refuses a NetDev of a unit's name or that creates a device
// of a WireGuard's name.
func withWireGuardUnits(d derive.Model, byKind, docs map[string]any) (map[string]any, error) {
	wgs, err := d.WireGuardInterfaces()
	if err != nil || len(wgs) == 0 {
		return docs, err
	}
	for _, w := range wgs {
		if unit := derive.WireGuardUnitPrefix + w.Name; documentNamed(byKind, "NetDev", unit) {
			return nil, refusals.Errorf("NetDev", unit, "metadata.name", "NetDev %s takes the name of WireGuard %s's unit; rename the document", unit, w.Name)
		}
		if name, _, at, ok := netDevCreating(byKind, func(device string) bool { return device == w.Name }); ok {
			return nil, refusals.Errorf("NetDev", name, at, "NetDev %s creates the device %s, the interface of WireGuard %s; give the device another name", name, w.Name, w.Name)
		}
	}
	netdevs, _ := docs["NetDev"].([]any)
	networks, _ := docs["Network"].([]any)
	netdevs, networks = slices.Clone(netdevs), slices.Clone(networks)
	for _, w := range wgs {
		what := "WireGuard " + w.Name
		key, err := credentialOf("key", w.Spec.PrivateKey)
		if err != nil {
			return nil, derive.Within(what, err)
		}
		var peers []any
		for _, p := range w.Peers {
			peer := map[string]any{
				"publicKey":  p.Spec.PublicKey,
				"allowedIPs": prefixStrings(p.Spec.AllowedIPs),
			}
			if p.Spec.Endpoint != nil {
				peer["endpoint"] = *p.Spec.Endpoint
			}
			if k := p.Spec.PersistentKeepalive; k != nil {
				peer["persistentKeepalive"] = number(*k)
			}
			if p.Spec.PresharedKey != nil {
				psk, err := credentialOf("key", *p.Spec.PresharedKey)
				if err != nil {
					return nil, derive.Within("WireGuardPeer "+p.Name, err)
				}
				peer["presharedKey"] = psk
			}
			peers = append(peers, peer)
		}
		meta := map[string]any{"name": derive.WireGuardUnitPrefix + w.Name}
		netdev := map[string]any{
			"netDev": map[string]any{"kind": "wireguard", "name": w.Name},
			"wireGuard": map[string]any{
				"listenPort": number(w.Spec.ListenPort),
				"privateKey": key,
				"routeTable": routeTable(w.Table),
			},
		}
		if len(peers) > 0 {
			netdev["wireGuardPeer"] = peers
		}
		netdevs = append(netdevs, map[string]any{"metadata": meta, "spec": netdev})
		network := map[string]any{"address": prefixStrings(w.Spec.Addresses)}
		if w.VRF != derive.DefaultVRF {
			network["vrf"] = w.VRF
		}
		spec := map[string]any{"match": map[string]any{"name": w.Name}, "network": network}
		if w.Spec.MTU != nil {
			spec["link"] = map[string]any{"mtuBytes": number(*w.Spec.MTU)}
		}
		networks = append(networks, map[string]any{"metadata": meta, "spec": spec})
	}
	out := maps.Clone(docs)
	out["NetDev"], out["Network"] = netdevs, networks
	return out, nil
}

// prefixStrings are the ranges as networkd reads them, each with its address as written.
func prefixStrings(ps []netip.Prefix) []any {
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

// routeTable is the value of RouteTable= that routes a WireGuard's allowed IPs in the table of its
// VRF: main for the main table, which networkd names so, else the VRF's table.
func routeTable(table uint32) any {
	if table == 254 {
		return "main"
	}
	return number(table)
}
