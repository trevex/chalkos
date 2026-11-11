package derive

import (
	"cmp"
	"encoding"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The specs of the router layer's kinds, as Build decodes them: with the defaults of their
// schemas, which give each field the type of its Go field.

// RouterSpec is a Router document's spec.
type RouterSpec struct {
	Forwarding struct {
		IPv4 bool `json:"ipv4"`
		IPv6 bool `json:"ipv6"`
	} `json:"forwarding"`
	RPFilter     RPFilter `json:"rpFilter"`
	ECMPHash     ECMPHash `json:"ecmpHash"`
	ConntrackMax *int64   `json:"conntrackMax"`
	Management   struct {
		// Interfaces is nil for every interface.
		Interfaces []string `json:"interfaces"`
	} `json:"management"`
}

// ZoneSpec is a FirewallZone document's spec.
type ZoneSpec struct {
	Interfaces []string `json:"interfaces"`
	// VRFs are the VRF documents whose links are all in the zone.
	VRFs []string `json:"vrfs"`
}

// PolicySpec is a FirewallPolicy document's spec.
type PolicySpec struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Default Verdict        `json:"default"`
	Rules   []FirewallRule `json:"rules"`
}

// FirewallRule is a rule of a FirewallPolicy.
type FirewallRule struct {
	Family           *Family     `json:"family"`
	Protocol         *Protocol   `json:"protocol"`
	SourcePorts      []PortRange `json:"sourcePorts"`
	DestinationPorts []PortRange `json:"destinationPorts"`
	Source           *Addresses  `json:"source"`
	Destination      *Addresses  `json:"destination"`
	ICMPTypes        []ICMPType  `json:"icmpTypes"`
	Action           Verdict     `json:"action"`
	Log              bool        `json:"log"`
}

// Addresses are a rule's source or destination: an AddressSet or address ranges, one of them; nil
// matches every address.
type Addresses struct {
	Set   *string        `json:"set"`
	CIDRs []netip.Prefix `json:"cidrs"`
}

// AddressSetSpec is an AddressSet document's spec.
type AddressSetSpec struct {
	Family   Family         `json:"family"`
	Prefixes []netip.Prefix `json:"prefixes"`
}

// NATSpec is a NATRule document's spec.
type NATSpec struct {
	Type   NATType   `json:"type"`
	Family Family    `json:"family"`
	Out    *NATPlace `json:"out"`
	In     *NATPlace `json:"in"`
	Match  struct {
		Source           []netip.Prefix `json:"source"`
		Destination      []netip.Prefix `json:"destination"`
		Protocol         *Protocol      `json:"protocol"`
		DestinationPorts []PortRange    `json:"destinationPorts"`
	} `json:"match"`
	To *struct {
		Address netip.Addr `json:"address"`
		Port    *int       `json:"port"`
	} `json:"to"`
}

// NATPlace is where a NATRule translates: out of or into a zone or an interface.
type NATPlace struct {
	Zone      *string `json:"zone"`
	Interface *string `json:"interface"`
}

// PortRange is a port from 1, or a range of ports "<first>-<last>", as nftables writes both. The
// schemas' format port-range checks the ports; UnmarshalJSON reads them again because the ruleset
// writes them unquoted, so that a value the schemas would refuse fails to decode rather than add
// syntax to the ruleset.
type PortRange string

func (p *PortRange) UnmarshalJSON(data []byte) error {
	var n uint16
	if err := json.Unmarshal(data, &n); err == nil && n > 0 {
		*p = PortRange(strconv.Itoa(int(n)))
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
	*p = PortRange(fmt.Sprintf("%d-%d", a, b))
	return nil
}

// bounds are the first and last port of p, which UnmarshalJSON has read as a port or a range
// "<first>-<last>" from 1 to 65535 that runs forwards.
func (p PortRange) bounds() (first, last int) {
	a, b, isRange := strings.Cut(string(p), "-")
	if !isRange {
		b = a
	}
	first, _ = strconv.Atoi(a)
	last, _ = strconv.Atoi(b)
	return first, last
}

// BGPSpec is a BGP document's spec.
type BGPSpec struct {
	// VRF names the instance's VRF document, or is default; VRFOf reads it.
	VRF       *string    `json:"vrf"`
	ASN       uint32     `json:"asn"`
	RouterID  netip.Addr `json:"routerID"`
	Multipath struct {
		EBGP        int  `json:"ebgp"`
		IBGP        int  `json:"ibgp"`
		ASPathRelax bool `json:"asPathRelax"`
	} `json:"multipath"`
	BFD struct {
		ReceiveMs  int `json:"receiveMs"`
		TransmitMs int `json:"transmitMs"`
		Multiplier int `json:"multiplier"`
	} `json:"bfd"`
	GracefulRestart bool `json:"gracefulRestart"`
	Networks        []struct {
		Prefix netip.Prefix `json:"prefix"`
		Policy *string      `json:"policy"`
	} `json:"networks"`
	Import struct {
		IPv4 *LeakSpec `json:"ipv4"`
		IPv6 *LeakSpec `json:"ipv6"`
	} `json:"import"`
}

// LeakSpec is what a BGP instance imports of one family from other VRFs' instances.
type LeakSpec struct {
	VRFs   []string `json:"vrfs"`
	Policy *string  `json:"policy"`
}

// PeerSpec is a BGPPeer document's spec.
type PeerSpec struct {
	// VRF names the peer's VRF document, or is default; VRFOf reads it.
	VRF       *string     `json:"vrf"`
	Address   *netip.Addr `json:"address"`
	Interface *string     `json:"interface"`
	// Interfaces is nil when the peer names an address, an interface or a listen range.
	Interfaces []string `json:"interfaces"`
	// Listen is nil unless the peer accepts the neighbours of a prefix.
	Listen   *PeerListen `json:"listen"`
	RemoteAS PeerAS      `json:"remoteAS"`
	Password *struct {
		SecretRef struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"secretRef"`
	} `json:"password"`
	BFD             bool `json:"bfd"`
	ExtendedNextHop bool `json:"extendedNextHop"`
	Families        struct {
		IPv4 *PeerFamily `json:"ipv4"`
		IPv6 *PeerFamily `json:"ipv6"`
	} `json:"families"`
	Description *string `json:"description"`
	Shutdown    bool    `json:"shutdown"`
}

// PeerListen is a BGPPeer's listen: the prefix whose neighbours may open a session.
type PeerListen struct {
	Range netip.Prefix `json:"range"`
}

// PeerFamily is what a BGPPeer exchanges of one address family.
type PeerFamily struct {
	Import           *string `json:"import"`
	Export           *string `json:"export"`
	DefaultOriginate bool    `json:"defaultOriginate"`
	// DefaultOriginateWhen is the PrefixList whose routes in the peer's VRF send the default
	// route; nil sends it whatever the table holds.
	DefaultOriginateWhen *string `json:"defaultOriginateWhen"`
	MaxPrefix            *uint32 `json:"maxPrefix"`
}

// PeerAS is a BGPPeer's remoteAS as FRR's remote-as takes it: external, internal or a number.
type PeerAS string

func (a *PeerAS) UnmarshalJSON(data []byte) error {
	var n uint32
	if err := json.Unmarshal(data, &n); err == nil && n > 0 {
		*a = PeerAS(strconv.FormatUint(uint64(n), 10))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil || (s != "external" && s != "internal") {
		return fmt.Errorf("remoteAS is external, internal or an AS number from 1 to 4294967295, not %s", data)
	}
	*a = PeerAS(s)
	return nil
}

// PrefixListSpec is a PrefixList document's spec.
type PrefixListSpec struct {
	Family  Family `json:"family"`
	Entries []struct {
		Prefix netip.Prefix `json:"prefix"`
		GE     *int         `json:"ge"`
		LE     *int         `json:"le"`
		Action ListAction   `json:"action"`
	} `json:"entries"`
}

// CommunityListSpec is a CommunityList document's spec.
type CommunityListSpec struct {
	Communities []Community `json:"communities"`
}

// RoutingPolicySpec is a RoutingPolicy document's spec.
type RoutingPolicySpec struct {
	Terms []struct {
		Match struct {
			PrefixList    *string `json:"prefixList"`
			CommunityList *string `json:"communityList"`
			ASPath        *string `json:"asPath"`
		} `json:"match"`
		Then struct {
			Action            RouteAction `json:"action"`
			LocalPref         *uint32     `json:"localPref"`
			MED               *uint32     `json:"med"`
			AddCommunities    []Community `json:"addCommunities"`
			RemoveCommunities []Community `json:"removeCommunities"`
			Prepend           *int        `json:"prepend"`
		} `json:"then"`
	} `json:"terms"`
	Default RouteAction `json:"default"`
}

// NAT64Spec is a NAT64 document's spec.
type NAT64Spec struct {
	// VRF names the NAT64's VRF document, or is default; VRFOf reads it.
	VRF    *string      `json:"vrf"`
	Prefix netip.Prefix `json:"prefix"`
	Pool4  []PoolEntry  `json:"pool4"`
}

// SecretRef names a key of a Secret, which a unit reads as its credential secret_<name>.<key>.
type SecretRef struct {
	SecretRef struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	} `json:"secretRef"`
}

// WireGuardSpec is a WireGuard document's spec.
type WireGuardSpec struct {
	ListenPort uint16         `json:"listenPort"`
	Addresses  []netip.Prefix `json:"addresses"`
	MTU        *uint32        `json:"mtu"`
	PrivateKey SecretRef      `json:"privateKey"`
	// VRF names the WireGuard's VRF document, or is default; VRFOf reads it.
	VRF *string `json:"vrf"`
}

// WireGuardPeerSpec is a WireGuardPeer document's spec.
type WireGuardPeerSpec struct {
	Interface           string         `json:"interface"`
	PublicKey           string         `json:"publicKey"`
	AllowedIPs          []netip.Prefix `json:"allowedIPs"`
	Endpoint            *string        `json:"endpoint"`
	PersistentKeepalive *uint16        `json:"persistentKeepalive"`
	PresharedKey        *SecretRef     `json:"presharedKey"`
}

// PoolEntry is an entry of a NAT64's pool4.
type PoolEntry struct {
	Prefix    netip.Prefix `json:"prefix"`
	Ports     PortRange    `json:"ports"`
	Protocols []string     `json:"protocols"`
}

// ResolverSpec is a Resolver document's spec.
type ResolverSpec struct {
	// VRF names the resolver's VRF document, or is default; VRFOf reads it.
	VRF        *string        `json:"vrf"`
	Listen     []netip.Addr   `json:"listen"`
	Allow      []netip.Prefix `json:"allow"`
	Forwarders []netip.Addr   `json:"forwarders"`
	DNSSEC     bool           `json:"dnssec"`
	DNS64      *struct {
		Prefix netip.Prefix `json:"prefix"`
	} `json:"dns64"`
	LocalZones []LocalZone `json:"localZones"`
}

// LocalZone is a local zone of a Resolver.
type LocalZone struct {
	Name    string        `json:"name"`
	Type    LocalZoneType `json:"type"`
	Records []struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
		TTL   uint32 `json:"ttl"`
	} `json:"records"`
}

// VRFSpec is a VRF document's spec.
type VRFSpec struct {
	Table      uint32   `json:"table"`
	Interfaces []string `json:"interfaces"`
	// ConntrackZone is nil for a VRF that shares zone 0 with the default VRF.
	ConntrackZone *int `json:"conntrackZone"`
}

// DHCPSubnetSpec is a DHCPSubnet document's spec.
type DHCPSubnetSpec struct {
	Interfaces []string     `json:"interfaces"`
	Subnet     netip.Prefix `json:"subnet"`
	Pools      []struct {
		First netip.Addr `json:"first"`
		Last  netip.Addr `json:"last"`
	} `json:"pools"`
	// Gateway, Domain and Boot are nil when the document leaves them out.
	Gateway      *netip.Addr  `json:"gateway"`
	DNS          []netip.Addr `json:"dns"`
	Domain       *string      `json:"domain"`
	LeaseTime    string       `json:"leaseTime"`
	Reservations []struct {
		MAC      string     `json:"mac"`
		Address  netip.Addr `json:"address"`
		Hostname *string    `json:"hostname"`
	} `json:"reservations"`
	Boot *struct {
		Server *netip.Addr `json:"server"`
		BIOS   *string     `json:"bios"`
		UEFI   *string     `json:"uefi"`
		IPXE   *string     `json:"ipxe"`
	} `json:"boot"`
	// PrefixDelegation is nil for a subnet that delegates no prefixes, as an IPv4 one.
	PrefixDelegation *struct {
		Prefix netip.Prefix `json:"prefix"`
		Length int          `json:"length"`
	} `json:"prefixDelegation"`
}

// DHCPRelaySpec is a DHCPRelay document's spec.
type DHCPRelaySpec struct {
	Interfaces []string     `json:"interfaces"`
	Servers    []netip.Addr `json:"servers"`
}

// PolicyRouteSpec is a PolicyRoute document's spec.
type PolicyRouteSpec struct {
	// In is nil for traffic from every link.
	In *struct {
		Zone       *string  `json:"zone"`
		Interfaces []string `json:"interfaces"`
	} `json:"in"`
	Rules []PolicyRule `json:"rules"`
}

// PolicyRule is a rule of a PolicyRoute.
type PolicyRule struct {
	Match struct {
		Family           *Family     `json:"family"`
		Protocol         *Protocol   `json:"protocol"`
		SourcePorts      []PortRange `json:"sourcePorts"`
		DestinationPorts []PortRange `json:"destinationPorts"`
		Source           *Addresses  `json:"source"`
		Destination      *Addresses  `json:"destination"`
		DSCP             []int       `json:"dscp"`
	} `json:"match"`
	To struct {
		VRF   *string `json:"vrf"`
		Table *uint32 `json:"table"`
		// NextHop names a NextHop document.
		NextHop *string `json:"nextHop"`
	} `json:"to"`
}

// NextHopSpec is a NextHop document's spec.
type NextHopSpec struct {
	Address   netip.Addr `json:"address"`
	Interface string     `json:"interface"`
	Table     uint32     `json:"table"`
}

// Each enum's UnmarshalJSON refuses a value its schema lacks; the outputs write some enums
// unquoted.

// Family is an address family.
type Family string

// The address families.
const (
	IPv4 Family = "ipv4"
	IPv6 Family = "ipv6"
)

func (f *Family) UnmarshalJSON(data []byte) error { return decodeEnum(data, f, IPv4, IPv6) }

// FamilyOf is the family of the address a.
func FamilyOf(a netip.Addr) Family {
	if a.Is4() {
		return IPv4
	}
	return IPv6
}

// RPFilter is a Router's reverse-path filter: off, loose or strict.
type RPFilter string

func (r *RPFilter) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, r, "off", "loose", "strict")
}

// ECMPHash is what a Router hashes to pick an equal-cost route: l3 or l4.
type ECMPHash string

func (h *ECMPHash) UnmarshalJSON(data []byte) error { return decodeEnum(data, h, "l3", "l4") }

// Verdict is what a FirewallPolicy or one of its rules does with a connection: accept, drop or
// reject.
type Verdict string

func (v *Verdict) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, v, "accept", "drop", "reject")
}

// Protocol is a protocol a firewall, NAT or policy routing rule matches.
type Protocol string

func (p *Protocol) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, p, "tcp", "udp", "sctp", "icmp", "icmpv6", "gre", "esp", "ah")
}

// ICMPType is an ICMP or ICMPv6 type a firewall rule matches, which nftables names alike for both
// protocols; packet-too-big is ICMPv6's alone.
type ICMPType string

func (t *ICMPType) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, t, "echo-request", "echo-reply", "destination-unreachable", "time-exceeded", "parameter-problem", "packet-too-big")
}

// NATType is how a NATRule translates: masquerade, snat or dnat.
type NATType string

func (t *NATType) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, t, "masquerade", "snat", "dnat")
}

// ListAction is whether a route a PrefixList entry matches matches the list: permit or deny.
type ListAction string

func (a *ListAction) UnmarshalJSON(data []byte) error { return decodeEnum(data, a, "permit", "deny") }

// RouteAction is what a RoutingPolicy does with a route: accept or reject.
type RouteAction string

func (a *RouteAction) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, a, "accept", "reject")
}

// LocalZoneType is how a Resolver answers the names of a local zone, as Unbound's local-zone
// types name it.
type LocalZoneType string

func (t *LocalZoneType) UnmarshalJSON(data []byte) error {
	return decodeEnum(data, t, "static", "transparent", "typetransparent", "redirect", "refuse", "deny", "always_nxdomain")
}

// decodeEnum decodes data, a JSON string, into v when it is one of values.
func decodeEnum[T ~string](data []byte, v *T, values ...T) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil || !slices.Contains(values, T(s)) {
		words := make([]string, len(values))
		for i, w := range values {
			words[i] = string(w)
		}
		return fmt.Errorf("%s is none of %s", data, strings.Join(words, ", "))
	}
	*v = T(s)
	return nil
}

// Community is a BGP community as FRR writes it: a standard one, <AS>:<value> of numbers of 16
// bits, or a large one, <AS>:<value>:<value> of numbers of 32 bits, in decimal without leading
// zeros. A value of another form fails to decode, so frr.conf, which writes it unquoted, never
// holds it.
type Community struct {
	Value string
	Large bool
}

func (c *Community) UnmarshalText(text []byte) error {
	parts := strings.Split(string(text), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("%q is no community <AS>:<value> or large community <AS>:<value>:<value>", text)
	}
	bits := 16
	if len(parts) == 3 {
		bits = 32
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, bits)
		if err != nil {
			return fmt.Errorf("%q is no community: its parts are numbers of %d bits", text, bits)
		}
		parts[i] = strconv.FormatUint(n, 10)
	}
	*c = Community{Value: strings.Join(parts, ":"), Large: len(parts) == 3}
	return nil
}

func (c Community) MarshalText() ([]byte, error) { return []byte(c.Value), nil }

func (c Community) String() string { return c.Value }

// The types checkDecoded reads: the decoders of a value, and the addresses and address ranges.
var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	addrType        = reflect.TypeFor[netip.Addr]()
	prefixType      = reflect.TypeFor[netip.Prefix]()
)

// decodes reports whether a field of type t decodes itself: an enum, a Community, a PeerAS, a
// PortRange, an address or an address range.
func decodes(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return t.Kind() != reflect.Pointer && (p.Implements(jsonUnmarshaler) || p.Implements(textUnmarshaler))
}

// checkDecoded refuses a value of v, a decoded spec at the JSON path path, that decodes itself and
// is missing, and an address with a zone. JSON leaves a field the input lacks at its zero value,
// which no such type decodes into, and netip decodes an empty string or null into an address or
// range that is not valid and takes a zone of any characters, which the outputs would write. The
// schemas' required fields, defaults and formats refuse each of them; a nil pointer is an
// optional field the input leaves out. The walk takes structs, pointers and slices, which the
// specs are made of, and refuses any other kind that holds a value.
func checkDecoded(path string, v reflect.Value) error {
	if t := v.Type(); decodes(t) {
		switch {
		case v.IsZero() && t == addrType:
			return fmt.Errorf("%s: missing address", path)
		case v.IsZero() && t == prefixType:
			return fmt.Errorf("%s: missing address range", path)
		case v.IsZero():
			return fmt.Errorf("%s: missing value", path)
		case t == addrType && v.Interface().(netip.Addr).Zone() != "":
			return fmt.Errorf("%s: %q has a zone", path, v.Interface().(netip.Addr))
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			return checkDecoded(path, v.Elem())
		}
	case reflect.Slice:
		for i := range v.Len() {
			if err := checkDecoded(fmt.Sprintf("%s[%d]", path, i), v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if err := checkDecoded(path+"."+cmp.Or(name, f.Name), v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Map, reflect.Array, reflect.Interface:
		if !v.IsZero() {
			return fmt.Errorf("%s: the specs hold no %s, which checkDecoded does not read", path, v.Kind())
		}
	}
	return nil
}
