package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/networkdschema"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
)

// Bridged is a node's config as chalkctl generates it from the cluster manifest, which renders
// each node's identity: the documents, the text chalkctl sends and its version.
type Bridged struct {
	Documents []Document
	// Text is the documents as a YAML stream: one JSON document each, separated by ---.
	Text string
	// Version is the hash of the documents Text holds, as chalkd computes it from the text.
	Version string
}

// FromManifest generates a node's config from the manifest: Cluster, Image, Node, TimeSync,
// SystemDisk and Volume, Network, NetDev and Link, and Kubelet documents mapped back from the
// identity the cluster definition rendered, then the documents the node's extensions add. The
// Image is named <role>-<platform>, and its source is the flake attribute path the manifest
// gives the role's image of the platform. Check tells whether the documents give back that
// identity.
func FromManifest(m *manifest.Manifest, name string) (Bridged, error) {
	n, ok := m.Nodes[name]
	if !ok {
		return Bridged{}, fmt.Errorf("the cluster %s has no node %s", m.Cluster.Name, name)
	}
	role, ok := m.Roles[n.Role]
	if !ok {
		return Bridged{}, fmt.Errorf("node %s runs the role %s, which the cluster does not have", name, n.Role)
	}
	id := n.Identity
	image := n.Role + "-" + n.Platform
	cluster := map[string]any{}
	if m.Cluster.Endpoint != "" {
		cluster["endpoint"] = m.Cluster.Endpoint
	}
	node := map[string]any{"imageRef": image}
	if role.Kind != "" {
		node["type"] = role.Kind
	}
	if id.Hostname != "" && id.Hostname != name {
		node["hostname"] = id.Hostname
	}
	objects := []map[string]any{
		object(CoreAPI, "Cluster", m.Cluster.Name, cluster),
		object(CoreAPI, "Image", image, map[string]any{"source": role.Images[n.Platform]}),
		object(CoreAPI, "Node", name, node),
		object(TimeAPI, "TimeSync", "time", map[string]any{"servers": timeServers(id.Time.Servers)}),
	}
	storageDocs, err := storageObjects(id.Storage)
	if err != nil {
		return Bridged{}, fmt.Errorf("node %s: %w", name, err)
	}
	objects = append(objects, storageDocs...)
	networkDocs, err := networkObjects(id.NetworkUnits)
	if err != nil {
		return Bridged{}, fmt.Errorf("node %s: %w", name, err)
	}
	objects = append(objects, networkDocs...)
	if id.Kubernetes != nil {
		objects = append(objects, kubeletObject(id))
	}
	return encode(objects)
}

func object(apiVersion, kind, name string, spec map[string]any) map[string]any {
	return map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}, "spec": spec}
}

// encode writes objects as the YAML stream chalkctl sends and reads it back as chalkd does, so
// the documents and their version are those of the text.
func encode(objects []map[string]any) (Bridged, error) {
	parts := make([]string, 0, len(objects))
	for _, o := range objects {
		b, err := json.Marshal(o)
		if err != nil {
			return Bridged{}, err
		}
		parts = append(parts, string(b))
	}
	text := strings.Join(parts, "\n---\n") + "\n"
	docs, err := Parse(strings.NewReader(text))
	if err != nil {
		return Bridged{}, err
	}
	version, err := Hash(docs)
	if err != nil {
		return Bridged{}, err
	}
	return Bridged{Documents: docs, Text: text, Version: version}, nil
}

func timeServers(servers []manifest.TimeServer) []any {
	out := []any{}
	for _, s := range servers {
		o := map[string]any{"host": s.Host, "nts": s.NTS}
		if s.Port != 0 {
			o["port"] = s.Port
		}
		out = append(out, o)
	}
	return out
}

// storageObjects maps a storage section back to its SystemDisk and Volume documents. Each
// volume names its encryption mode, and its format as null for a raw volume, since the kind's
// defaults would give other values.
func storageObjects(sec storage.Section) ([]map[string]any, error) {
	varVolume := sec.Volumes[storage.VarVolume]
	varSpec := map[string]any{"encryption": map[string]any{"mode": varVolume.Encryption}}
	if varVolume.Size != "" {
		varSpec["size"] = varVolume.Size
	}
	objects := []map[string]any{object(StorageAPI, "SystemDisk", "system", map[string]any{
		"disk":       diskObject(sec.Disks[storage.SystemDisk].Ref),
		"encryption": map[string]any{"mode": sec.Encryption, "fallback": sec.Fallback},
		"var":        varSpec,
	})}
	for _, name := range slices.Sorted(maps.Keys(sec.Volumes)) {
		if name == storage.VarVolume {
			continue
		}
		v := sec.Volumes[name]
		spec := map[string]any{"format": nil, "encryption": map[string]any{"mode": v.Encryption}}
		if v.Format != "" {
			spec["format"] = v.Format
		}
		if v.Size != "" {
			spec["size"] = v.Size
		}
		if v.MountPoint != "" {
			spec["mountPoint"] = v.MountPoint
		}
		disk, file := storage.SystemDisk, "60-"+name+".conf"
		if v.Disk != storage.SystemDisk {
			disk, file = v.Disk, "10-"+name+".conf"
			spec["disk"] = diskObject(sec.Disks[disk].Ref)
		}
		def, ok := sec.Disks[disk].Repart[file]
		if !ok {
			return nil, fmt.Errorf("volume %s: disk %s has no definition %s", name, disk, file)
		}
		repart, err := passthrough(def)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %s: %w", name, file, err)
		}
		if len(repart) > 0 {
			spec["repart"] = repart
		}
		objects = append(objects, object(StorageAPI, "Volume", name, spec))
	}
	return objects, nil
}

func diskObject(ref storage.Ref) map[string]any {
	if ref.Path != "" {
		return map[string]any{"path": ref.Path}
	}
	out := map[string]any{}
	for key, v := range map[string]string{"model": ref.Selector.Model, "serial": ref.Selector.Serial,
		"wwn": ref.Selector.WWN, "size": ref.Selector.Size, "type": ref.Selector.Type} {
		if v != "" {
			out[key] = v
		}
	}
	return out
}

// passthrough returns the keys of a volume's repart definition that its typed fields do not
// render: all but the reserved keys, and TPM2PCRs too when the volume is encrypted with the
// TPM, which sets it.
func passthrough(def string) (map[string]any, error) {
	sections, err := parseINI(def)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, s := range sections {
		if s.name != "Partition" {
			return nil, fmt.Errorf("section [%s] is not [Partition]", s.name)
		}
		encrypted := slices.Contains(s.entries, [2]string{"Encrypt", "tpm2"})
		for _, e := range s.entries {
			if slices.Contains(reservedRepartKeys, e[0]) || encrypted && e[0] == "TPM2PCRs" {
				continue
			}
			out[e[0]] = e[1]
		}
	}
	return out, nil
}

// networkObjects parses rendered networkd units back into Network, NetDev and Link documents,
// spelling sections and keys as the schemas do. A section or key that appears more than once
// becomes a list in the order of the unit.
func networkObjects(units map[string]string) ([]map[string]any, error) {
	var objects []map[string]any
	for _, file := range slices.Sorted(maps.Keys(units)) {
		kind, name := unitKind(file)
		if kind == "" {
			return nil, fmt.Errorf("network unit %s is not a .network, .netdev or .link file", file)
		}
		sections, err := parseINI(units[file])
		if err != nil {
			return nil, fmt.Errorf("network unit %s: %w", file, err)
		}
		spec := map[string]any{}
		for _, s := range sections {
			body := map[string]any{}
			for _, e := range s.entries {
				key := networkdschema.CamelCase(e[0])
				body[key] = appendValue(body[key], e[1])
			}
			section := networkdschema.CamelCase(s.name)
			spec[section] = appendValue(spec[section], body)
		}
		objects = append(objects, object(NetworkdAPI, kind, name, spec))
	}
	return objects, nil
}

func unitKind(file string) (kind, name string) {
	for k, suffix := range unitSuffix {
		if n, ok := strings.CutSuffix(file, suffix); ok {
			return k, n
		}
	}
	return "", ""
}

// appendValue adds v to what a key or section holds so far: v alone the first time, a list of
// every value once there are more.
func appendValue(prev, v any) any {
	switch p := prev.(type) {
	case nil:
		return v
	case []any:
		return append(p, v)
	default:
		return []any{p, v}
	}
}

func kubeletObject(id manifest.Identity) map[string]any {
	labels := map[string]any{}
	for k, v := range id.Labels {
		labels[k] = v
	}
	taints := []any{}
	for _, t := range id.Taints {
		o := map[string]any{"key": t.Key, "effect": t.Effect}
		if t.Value != "" {
			o["value"] = t.Value
		}
		taints = append(taints, o)
	}
	spec := map[string]any{"labels": labels, "taints": taints, "nodeIPs": stringList(id.Kubernetes.NodeIPs)}
	if id.Kubernetes.ValidSubnets != nil {
		spec["validSubnets"] = stringList(id.Kubernetes.ValidSubnets)
	}
	return object(KubernetesAPI, "Kubelet", "kubelet", spec)
}

func stringList(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

// iniSection is a section of a unit file or repart definition, with its key=value lines in order.
type iniSection struct {
	name    string
	entries [][2]string
}

// parseINI reads the sections of a systemd-style file. Blank lines and comments are skipped;
// \= in a key stands for =, as repart definitions escape it.
func parseINI(text string) ([]iniSection, error) {
	var out []iniSection
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.TrimSpace(line) == "", strings.HasPrefix(line, "#"), strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			out = append(out, iniSection{name: line[1 : len(line)-1]})
		default:
			key, value, ok := cutKey(line)
			if !ok || len(out) == 0 {
				return nil, fmt.Errorf("line %d: %q is neither a section nor a key=value line of one", i+1, line)
			}
			out[len(out)-1].entries = append(out[len(out)-1].entries, [2]string{key, value})
		}
	}
	return out, nil
}

// cutKey splits a key=value line at the first = that \ does not escape.
func cutKey(line string) (key, value string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '=' {
			i++
			continue
		}
		if line[i] == '=' {
			return strings.ReplaceAll(line[:i], `\=`, "="), line[i+1:], true
		}
	}
	return "", "", false
}

// sameINI tells whether two files hold the same sections with the same lines, in any order.
func sameINI(a, b string) bool {
	sa, errA := parseINI(a)
	sb, errB := parseINI(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return slices.Equal(iniLines(sa), iniLines(sb))
}

func iniLines(sections []iniSection) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		lines := make([]string, 0, len(s.entries))
		for _, e := range s.entries {
			lines = append(lines, e[0]+"="+e[1])
		}
		sort.Strings(lines)
		out = append(out, "["+s.name+"]\n"+strings.Join(lines, "\n"))
	}
	sort.Strings(out)
	return out
}

// StaticAddresses returns the addresses Set.Identity counts as the node's static ones, in network
// name order and without prefix lengths, read from the documents alone, so a client finds a node
// before it has the schemas of the node's image.
func StaticAddresses(docs []Document) []string {
	return manifest.Identity{Network: NetworkMap(docs)}.StaticAddresses()
}

// Check translates the documents for the image t describes, with its schemas, and requires the
// identity the cluster definition rendered for the node, so chalkctl never delivers config that
// means something else on the node than the definition says. It returns the translated identity.
func (b Bridged) Check(want manifest.Identity, s *schema.Schemas, t Target) (manifest.Identity, error) {
	set, err := NewSet(b.Documents, s)
	if err != nil {
		return manifest.Identity{}, err
	}
	got, err := set.Identity(t)
	if err != nil {
		return manifest.Identity{}, err
	}
	if diffs := identityDiff(want, got); len(diffs) > 0 {
		return manifest.Identity{}, fmt.Errorf("the config generated from the cluster definition does not give back its identity: %s", strings.Join(diffs, "; "))
	}
	return got, nil
}

// identityDiff names what of got differs from want, compared by meaning: unit files and repart
// definitions as sections of lines in any order, and of the network map only what Go reads, the
// static addresses and the MAC address each network matches.
func identityDiff(want, got manifest.Identity) []string {
	var diffs []string
	differ := func(what string, w, g any) {
		diffs = append(diffs, fmt.Sprintf("%s %+v instead of %+v", what, g, w))
	}
	if want.Hostname != got.Hostname {
		differ("hostname", want.Hostname, got.Hostname)
	}
	if want.Cluster != got.Cluster || want.Role != got.Role || want.Platform != got.Platform {
		differ("cluster, role and platform", []string{want.Cluster, want.Role, want.Platform}, []string{got.Cluster, got.Role, got.Platform})
	}
	if w, g := want.StaticAddresses(), got.StaticAddresses(); !slices.Equal(w, g) {
		differ("static addresses", w, g)
	}
	if w, g := matchedMACs(want), matchedMACs(got); !maps.Equal(w, g) {
		differ("matched MAC addresses", w, g)
	}
	if !sameFiles(want.NetworkUnits, got.NetworkUnits) {
		differ("network units", want.NetworkUnits, got.NetworkUnits)
	}
	if !maps.Equal(want.Labels, got.Labels) {
		differ("labels", want.Labels, got.Labels)
	}
	if !slices.Equal(want.Taints, got.Taints) {
		differ("taints", want.Taints, got.Taints)
	}
	if !sameStorage(want.Storage, got.Storage) {
		differ("storage", want.Storage, got.Storage)
	}
	if !sameKubernetes(want.Kubernetes, got.Kubernetes) {
		differ("kubernetes", want.Kubernetes, got.Kubernetes)
	}
	if !slices.Equal(want.Time.Servers, got.Time.Servers) {
		differ("time servers", want.Time.Servers, got.Time.Servers)
	}
	return diffs
}

func matchedMACs(id manifest.Identity) map[string]string {
	out := map[string]string{}
	networks, _ := id.Network["networks"].(map[string]any)
	for name, n := range networks {
		network, _ := n.(map[string]any)
		match, _ := network["matchConfig"].(map[string]any)
		if mac, ok := match["MACAddress"].(string); ok {
			out[name] = mac
		}
	}
	return out
}

// sameFiles compares files by name, each with sameINI.
func sameFiles(want, got map[string]string) bool {
	if len(want) != len(got) {
		return false
	}
	for name, w := range want {
		if g, ok := got[name]; !ok || !sameINI(w, g) {
			return false
		}
	}
	return true
}

func sameStorage(want, got storage.Section) bool {
	if want.Encryption != got.Encryption || want.Fallback != got.Fallback || !maps.Equal(want.Volumes, got.Volumes) || len(want.Disks) != len(got.Disks) {
		return false
	}
	for name, w := range want.Disks {
		g, ok := got.Disks[name]
		if !ok || w.Ref != g.Ref || w.Seed != g.Seed || !sameFiles(w.Repart, g.Repart) {
			return false
		}
	}
	return true
}

// sameKubernetes compares the Kubernetes sections; nil validSubnets, which use the cluster's,
// differ from empty ones.
func sameKubernetes(want, got *manifest.KubernetesIdentity) bool {
	if want == nil || got == nil {
		return want == got
	}
	return want.NodeName == got.NodeName && slices.Equal(want.NodeIPs, got.NodeIPs) &&
		(want.ValidSubnets == nil) == (got.ValidSubnets == nil) && slices.Equal(want.ValidSubnets, got.ValidSubnets)
}
