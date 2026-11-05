package derive

import (
	"encoding/json"
	"fmt"
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
	RPFilter     string `json:"rpFilter"`
	ECMPHash     string `json:"ecmpHash"`
	ConntrackMax *int64 `json:"conntrackMax"`
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
	Default string         `json:"default"`
	Rules   []FirewallRule `json:"rules"`
}

// FirewallRule is a rule of a FirewallPolicy.
type FirewallRule struct {
	Family           *string     `json:"family"`
	Protocol         *string     `json:"protocol"`
	SourcePorts      []PortRange `json:"sourcePorts"`
	DestinationPorts []PortRange `json:"destinationPorts"`
	Source           *Addresses  `json:"source"`
	Destination      *Addresses  `json:"destination"`
	ICMPTypes        []string    `json:"icmpTypes"`
	Action           string      `json:"action"`
	Log              bool        `json:"log"`
}

// Addresses are a rule's source or destination: an AddressSet or address ranges, one of them; nil
// matches every address.
type Addresses struct {
	Set   *string  `json:"set"`
	CIDRs []string `json:"cidrs"`
}

// AddressSetSpec is an AddressSet document's spec.
type AddressSetSpec struct {
	Family   string   `json:"family"`
	Prefixes []string `json:"prefixes"`
}

// NATSpec is a NATRule document's spec.
type NATSpec struct {
	Type   string    `json:"type"`
	Family string    `json:"family"`
	Out    *NATPlace `json:"out"`
	In     *NATPlace `json:"in"`
	Match  struct {
		Source           []string    `json:"source"`
		Destination      []string    `json:"destination"`
		Protocol         *string     `json:"protocol"`
		DestinationPorts []PortRange `json:"destinationPorts"`
	} `json:"match"`
	To *struct {
		Address string `json:"address"`
		Port    *int   `json:"port"`
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
	VRF       *string `json:"vrf"`
	ASN       uint32  `json:"asn"`
	RouterID  string  `json:"routerID"`
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
		Prefix string  `json:"prefix"`
		Policy *string `json:"policy"`
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
	VRF       *string `json:"vrf"`
	Address   *string `json:"address"`
	Interface *string `json:"interface"`
	// Interfaces is nil when the peer names an address or an interface.
	Interfaces []string `json:"interfaces"`
	RemoteAS   PeerAS   `json:"remoteAS"`
	Password   *struct {
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
	Family  string `json:"family"`
	Entries []struct {
		Prefix string `json:"prefix"`
		GE     *int   `json:"ge"`
		LE     *int   `json:"le"`
		Action string `json:"action"`
	} `json:"entries"`
}

// CommunityListSpec is a CommunityList document's spec.
type CommunityListSpec struct {
	Communities []string `json:"communities"`
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
			Action            string   `json:"action"`
			LocalPref         *uint32  `json:"localPref"`
			MED               *uint32  `json:"med"`
			AddCommunities    []string `json:"addCommunities"`
			RemoveCommunities []string `json:"removeCommunities"`
			Prepend           *int     `json:"prepend"`
		} `json:"then"`
	} `json:"terms"`
	Default string `json:"default"`
}

// NAT64Spec is a NAT64 document's spec.
type NAT64Spec struct {
	// VRF names the NAT64's VRF document, or is default; VRFOf reads it.
	VRF    *string     `json:"vrf"`
	Prefix string      `json:"prefix"`
	Pool4  []PoolEntry `json:"pool4"`
}

// PoolEntry is an entry of a NAT64's pool4.
type PoolEntry struct {
	Prefix    string    `json:"prefix"`
	Ports     PortRange `json:"ports"`
	Protocols []string  `json:"protocols"`
}

// ResolverSpec is a Resolver document's spec.
type ResolverSpec struct {
	// VRF names the resolver's VRF document, or is default; VRFOf reads it.
	VRF        *string  `json:"vrf"`
	Listen     []string `json:"listen"`
	Allow      []string `json:"allow"`
	Forwarders []string `json:"forwarders"`
	DNSSEC     bool     `json:"dnssec"`
	DNS64      *struct {
		Prefix string `json:"prefix"`
	} `json:"dns64"`
	LocalZones []LocalZone `json:"localZones"`
}

// LocalZone is a local zone of a Resolver.
type LocalZone struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
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
	Interfaces []string `json:"interfaces"`
	Subnet     string   `json:"subnet"`
	Pools      []struct {
		First string `json:"first"`
		Last  string `json:"last"`
	} `json:"pools"`
	// Gateway, Domain and Boot are nil when the document leaves them out.
	Gateway      *string  `json:"gateway"`
	DNS          []string `json:"dns"`
	Domain       *string  `json:"domain"`
	LeaseTime    string   `json:"leaseTime"`
	Reservations []struct {
		MAC      string  `json:"mac"`
		Address  string  `json:"address"`
		Hostname *string `json:"hostname"`
	} `json:"reservations"`
	Boot *struct {
		Server *string `json:"server"`
		BIOS   *string `json:"bios"`
		UEFI   *string `json:"uefi"`
		IPXE   *string `json:"ipxe"`
	} `json:"boot"`
	// PrefixDelegation is nil for a subnet that delegates no prefixes, as an IPv4 one.
	PrefixDelegation *struct {
		Prefix string `json:"prefix"`
		Length int    `json:"length"`
	} `json:"prefixDelegation"`
}

// DHCPRelaySpec is a DHCPRelay document's spec.
type DHCPRelaySpec struct {
	Interfaces []string `json:"interfaces"`
	Servers    []string `json:"servers"`
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
		Family           *string     `json:"family"`
		Protocol         *string     `json:"protocol"`
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
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Table     uint32 `json:"table"`
}
