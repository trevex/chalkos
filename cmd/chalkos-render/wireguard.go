package main

import (
	"encoding/json"
	"maps"
	"net/netip"
	"slices"
	"strconv"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// withWireGuardUnits is docs, the networkd documents with the VRFs' and NAT64s' units, with a
// NetDev and a Network document 00-wg-<name> per WireGuard, as derive.Model.WireGuardInterfaces
// checks them. The NetDev creates the interface with its port, its private key, a
// [WireGuardPeer] section per peer and RouteTable=, which routes the peers' allowed IPs to the
// interface in the table of its VRF, and the Network gives the link its addresses, its VRF and its
// MTU. The keys are networkd's credentials @secret_<secret>.<key>, which chalkd loads from
// the Secrets, so the units hold no key. It refuses a NetDev of byKind of a unit's name or that
// creates a device of a WireGuard's name.
func withWireGuardUnits(d derive.Model, byKind, docs map[string]any) (map[string]any, error) {
	wgs, err := d.WireGuardInterfaces()
	if err != nil || len(wgs) == 0 {
		return docs, err
	}
	existing, _ := byKind["NetDev"].([]any)
	for _, w := range wgs {
		unit := derive.WireGuardUnitPrefix + w.Name
		for _, e := range existing {
			doc, _ := e.(map[string]any)
			name, _ := derive.DocumentName("NetDev", doc)
			if name == unit {
				return nil, refusals.Errorf("NetDev", name, "metadata.name", "NetDev %s takes the name of WireGuard %s's unit; rename the document", name, w.Name)
			}
			spec, _ := doc["spec"].(map[string]any)
			for si, s := range derive.OneOrMany(spec["netDev"]) {
				section, _ := s.(map[string]any)
				for ni, device := range derive.OneOrMany(section["name"]) {
					if device == w.Name {
						at := derive.ItemPath(derive.ItemPath("spec.netDev", spec["netDev"], si)+".name", section["name"], ni)
						return nil, refusals.Errorf("NetDev", name, at, "NetDev %s creates the device %s, the interface of WireGuard %s; give the device another name", name, w.Name, w.Name)
					}
				}
			}
		}
	}
	netdevs, _ := docs["NetDev"].([]any)
	networks, _ := docs["Network"].([]any)
	netdevs, networks = slices.Clone(netdevs), slices.Clone(networks)
	for _, w := range wgs {
		what := "WireGuard " + w.Name
		key, err := credentialOf(w.Spec.PrivateKey)
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
				peer["persistentKeepalive"] = json.Number(strconv.FormatUint(uint64(*k), 10))
			}
			if p.Spec.PresharedKey != nil {
				psk, err := credentialOf(*p.Spec.PresharedKey)
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
				"listenPort": json.Number(strconv.FormatUint(uint64(w.Spec.ListenPort), 10)),
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
			spec["link"] = map[string]any{"mtuBytes": json.Number(strconv.FormatUint(uint64(*w.Spec.MTU), 10))}
		}
		networks = append(networks, map[string]any{"metadata": meta, "spec": spec})
	}
	out := maps.Clone(docs)
	out["NetDev"], out["Network"] = netdevs, networks
	return out, nil
}

// credentialOf is the value of a WireGuard key's setting that names the Secret's key as
// systemd-networkd's credential, @secret_<secret>.<key>. The kinds' rules hold the names to
// credentials' characters; another is an internal error.
func credentialOf(r derive.SecretRef) (string, error) {
	name, key := r.SecretRef.Name, r.SecretRef.Key
	if !credential.MatchString(name) || !credential.MatchString(key) {
		return "", derive.InternalErrorf("the key's Secret %q and key %q are letters, digits, '.', '_' and '-' alone", name, key)
	}
	return "@secret_" + name + "." + key, nil
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
	return json.Number(strconv.FormatUint(uint64(table), 10))
}
