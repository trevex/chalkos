// Package keaconf is the part of the configs of Kea's DHCPv4 and DHCPv6 servers that the output
// kea writes: the renderer marshals these types, and chalkd reads the rendered files back into
// them. Fields Kea takes in either server sit in the types both servers' sections embed. It takes
// the standard library alone, as chalkos-render does.
package keaconf

import "net/netip"

// Config is a config file of Kea: the section of the DHCPv4 server or that of the DHCPv6 server.
type Config struct {
	Dhcp4 *Dhcp4 `json:"Dhcp4,omitempty"`
	Dhcp6 *Dhcp6 `json:"Dhcp6,omitempty"`
}

// Server is what the sections of both servers hold: the links and how to open their sockets,
// the control socket, the leases, the hooks and the loggers.
type Server struct {
	InterfacesConfig InterfacesConfig `json:"interfaces-config"`
	ControlSockets   []ControlSocket  `json:"control-sockets,omitempty"`
	LeaseDatabase    *LeaseDatabase   `json:"lease-database,omitempty"`
	HooksLibraries   []Hook           `json:"hooks-libraries,omitempty"`
	Loggers          []Logger         `json:"loggers,omitempty"`
}

// Dhcp4 is the section of the DHCPv4 server.
type Dhcp4 struct {
	Server
	ClientClasses []ClientClass `json:"client-classes,omitempty"`
	Subnet4       []Subnet4     `json:"subnet4"`
}

// Dhcp6 is the section of the DHCPv6 server.
type Dhcp6 struct {
	Server
	Subnet6 []Subnet6 `json:"subnet6"`
}

// InterfacesConfig names the links the server answers on. A section without Interfaces, not even
// an empty list, is no config Kea starts with.
type InterfacesConfig struct {
	Interfaces []string `json:"interfaces"`
	// DHCPSocketType is raw or udp, of the DHCPv4 server alone.
	DHCPSocketType string `json:"dhcp-socket-type,omitempty"`
	// RetryWaitTime, in milliseconds, and MaxRetries are how often Kea tries again to open the
	// sockets of a link.
	RetryWaitTime int    `json:"service-sockets-retry-wait-time,omitempty"`
	MaxRetries    uint32 `json:"service-sockets-max-retries,omitempty"`
}

// ControlSocket is a socket that takes Kea's commands.
type ControlSocket struct {
	SocketType string `json:"socket-type"`
	SocketName string `json:"socket-name"`
}

// LeaseDatabase is where the server keeps its leases.
type LeaseDatabase struct {
	Type        string `json:"type"`
	Persist     bool   `json:"persist"`
	Name        string `json:"name"`
	LFCInterval int    `json:"lfc-interval"`
}

// Hook is a hook library the server loads.
type Hook struct {
	Library    string          `json:"library"`
	Parameters *HookParameters `json:"parameters,omitempty"`
}

// HookParameters are the parameters of the run_script hook: the program it runs.
type HookParameters struct {
	Name string `json:"name"`
}

// Logger sends the messages of a logger from Severity up to its outputs.
type Logger struct {
	Name          string         `json:"name"`
	Severity      string         `json:"severity"`
	OutputOptions []OutputOption `json:"output-options"`
}

// OutputOption is an output of a logger and the pattern of its lines.
type OutputOption struct {
	Output  string `json:"output"`
	Pattern string `json:"pattern"`
}

// ClientClass is a client class of the DHCPv4 server: the clients Test matches, and the boot
// file and next server they get.
type ClientClass struct {
	Name                 string `json:"name"`
	OnlyInAdditionalList bool   `json:"only-in-additional-list"`
	Test                 string `json:"test"`
	BootFileName         string `json:"boot-file-name"`
	NextServer           string `json:"next-server,omitempty"`
}

// Subnet is what the subnets of both servers hold: Kea's number for it, its range, its lease
// time, its pools, options and reservations, and the name of its DHCPSubnet document.
type Subnet struct {
	ID            uint32        `json:"id"`
	Subnet        string        `json:"subnet"`
	ValidLifetime uint64        `json:"valid-lifetime"`
	Pools         []Pool        `json:"pools,omitempty"`
	OptionData    []Option      `json:"option-data,omitempty"`
	Reservations  []Reservation `json:"reservations,omitempty"`
	UserContext   UserContext   `json:"user-context"`
}

// Subnet4 is a subnet of the DHCPv4 server, with the client classes Kea evaluates once it chose
// the subnet.
type Subnet4 struct {
	Subnet
	EvaluateAdditionalClasses []string `json:"evaluate-additional-classes,omitempty"`
}

// Subnet6 is a subnet of the DHCPv6 server: its link, how long clients prefer their addresses and
// the prefixes it delegates.
type Subnet6 struct {
	Subnet
	Interface         string   `json:"interface"`
	PreferredLifetime uint64   `json:"preferred-lifetime"`
	PDPools           []PDPool `json:"pd-pools,omitempty"`
}

// UserContext is what Kea keeps of a subnet for others to read: the name of its document.
type UserContext struct {
	Name string `json:"name"`
}

// Pool is a range of addresses, "<first> - <last>".
type Pool struct {
	Pool string `json:"pool"`
}

// PDPool is a range of prefixes the DHCPv6 server delegates, Prefix/PrefixLen, in prefixes of
// DelegatedLen.
type PDPool struct {
	Prefix       netip.Addr `json:"prefix"`
	PrefixLen    int        `json:"prefix-len"`
	DelegatedLen int        `json:"delegated-len"`
}

// Option is an option the server sends, by name, with its values as Kea's text.
type Option struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

// Reservation is an address of the client with the hardware address: IPAddress of the DHCPv4
// server, IPAddresses of the DHCPv6 server.
type Reservation struct {
	HWAddress   string   `json:"hw-address"`
	IPAddress   string   `json:"ip-address,omitempty"`
	IPAddresses []string `json:"ip-addresses,omitempty"`
	Hostname    *string  `json:"hostname,omitempty"`
}
