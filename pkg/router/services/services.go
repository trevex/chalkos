// Package services names the files, systemd units and runtime directories of the router's services
// that run once per VRF: Kea's DHCPv4 and DHCPv6 servers, Unbound and dnsmasq's DHCP relay. The
// renderer writes the configs by these names, the modules in modules/node/router run the units by
// them, and chalkd reads them back. It takes the standard library alone, as chalkos-render does.
package services

import (
	"fmt"
	"strings"
)

// defaultVRF names the VRF of every link no VRF holds, as derive.DefaultVRF does.
const defaultVRF = "default"

// Service is a router service of one instance per VRF. The default VRF's instance runs as
// <Name>.service from the config <Name><Ext>, a VRF's as <Name>@<vrf>.service from
// <Name>-<vrf><Ext>, with systemd's sockets bound to the VRF's device.
type Service struct {
	// Name is the default VRF's unit's name, without .service, and its config's stem.
	Name string
	// Ext is the configs' extension, with its dot.
	Ext string
	// Runtime is the default VRF's runtime directory in /run.
	Runtime string
	// RuntimePerVRF gives a VRF's instance a runtime directory <Runtime>-<vrf> of its own; the
	// instances of a service without it share Runtime.
	RuntimePerVRF bool
}

// The router's per-VRF services.
var (
	Kea4    = Service{Name: "kea-dhcp4", Ext: ".json", Runtime: "kea"}
	Kea6    = Service{Name: "kea-dhcp6", Ext: ".json", Runtime: "kea"}
	Unbound = Service{Name: "unbound", Ext: ".conf", Runtime: "unbound", RuntimePerVRF: true}
	Relay   = Service{Name: "dnsmasq-relay", Ext: ".conf", Runtime: "dnsmasq-relay"}
)

// stem is the name of the instance of vrf: Name for the default VRF, Name-<vrf> for a VRF.
func (s Service) stem(vrf string) string {
	if vrf == defaultVRF {
		return s.Name
	}
	return s.Name + "-" + vrf
}

// File is the config file of the instance of vrf.
func (s Service) File(vrf string) string {
	return s.stem(vrf) + s.Ext
}

// VRFOfFile is the VRF whose instance's config file is name, and false for a name that is no
// config of the service.
func (s Service) VRFOfFile(name string) (vrf string, ok bool) {
	stem, ok := strings.CutSuffix(name, s.Ext)
	if !ok {
		return "", false
	}
	if stem == s.Name {
		return defaultVRF, true
	}
	vrf, ok = strings.CutPrefix(stem, s.Name+"-")
	if !ok || vrf == "" || vrf == defaultVRF {
		return "", false
	}
	return vrf, true
}

// Unit is the systemd unit of the instance of vrf.
func (s Service) Unit(vrf string) string {
	if vrf == defaultVRF {
		return s.Name + ".service"
	}
	return s.Name + "@" + escape(vrf) + ".service"
}

// RuntimeDir is the runtime directory of the instance of vrf, a name in /run.
func (s Service) RuntimeDir(vrf string) string {
	if s.RuntimePerVRF && vrf != defaultVRF {
		return s.Runtime + "-" + vrf
	}
	return s.Runtime
}

// IndexRecord is the file in which the instance of vrf keeps the index of the VRF's device that
// systemd bound its sockets to, <RuntimeDir>/<Name>-<vrf>.ifindex, a path in /run. The default
// VRF's instance is bound to no device and keeps none: it is "".
func (s Service) IndexRecord(vrf string) string {
	if vrf == defaultVRF {
		return ""
	}
	return s.RuntimeDir(vrf) + "/" + s.stem(vrf) + ".ifindex"
}

// escape is s as systemd-escape escapes a unit's instance: ASCII letters, digits, ':', '_' and
// every '.' after the first byte stay as they are, '/' becomes '-', and every other byte becomes
// \x and its two hex digits.
func escape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '.' && i > 0, c == ':', c == '_',
			'0' <= c && c <= '9', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}
