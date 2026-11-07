package translate

import (
	"net/netip"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// nodeSpec is the Kubernetes Node's.
type nodeSpec struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
	Taints []struct {
		Key    string  `json:"key"`
		Value  *string `json:"value"`
		Effect string  `json:"effect"`
	} `json:"taints"`
	KubeletExtraArgs map[string]string `json:"kubeletExtraArgs"`
	// NodeIP is how the node gets its addresses: nil Addresses take the static ones, nil
	// ValidSubnets the Cluster's.
	NodeIP struct {
		Addresses    []string `json:"addresses"`
		ValidSubnets []string `json:"validSubnets"`
	} `json:"nodeIP"`
	// VIP.Interface is the interface a control plane adds the Cluster's VIP to; nil takes the one
	// holding the node's address of the VIP's family.
	VIP struct {
		Interface *string `json:"interface"`
	} `json:"vip"`
}

// clusterSpec is the Kubernetes Cluster's. The schema's enum and its formats ip and cidr have
// checked the families, addresses and ranges, so they decode into their types; a value no schema
// checked fails the decode.
type clusterSpec struct {
	Endpoint                       string                            `json:"endpoint"`
	Domain                         string                            `json:"domain"`
	AllowSchedulingOnControlPlanes bool                              `json:"allowSchedulingOnControlPlanes"`
	IPFamilies                     []nodeip.Family                   `json:"ipFamilies"`
	PodCIDRs                       kubernetes.ByFamily[netip.Prefix] `json:"podCIDRs"`
	ServiceCIDRs                   kubernetes.ByFamily[netip.Prefix] `json:"serviceCIDRs"`
	DNSIPs                         kubernetes.ByFamily[*netip.Addr]  `json:"dnsIPs"`
	NodeCIDRMaskSizes              kubernetes.ByFamily[int]          `json:"nodeCIDRMaskSizes"`
	NodeIP                         kubernetes.NodeIP                 `json:"nodeIP"`
	VIP                            struct {
		Addresses []netip.Addr `json:"addresses"`
		Mode      string       `json:"mode"`
	} `json:"vip"`
	ExtraArgs map[string]map[string]string `json:"extraArgs"`
	Images    clusterImages                `json:"images"`
}

// clusterImages is the Cluster's images block.
type clusterImages struct {
	APIServer         string `json:"apiServer"`
	ControllerManager string `json:"controllerManager"`
	Scheduler         string `json:"scheduler"`
	Proxy             string `json:"proxy"`
	Etcd              string `json:"etcd"`
	CoreDNS           string `json:"coredns"`
	Pause             string `json:"pause"`
	// Flannel is empty on an image without flannel, whose schema has no such field.
	Flannel string `json:"flannel"`
}

// images are the cluster's images as the cluster file names them.
func (i clusterImages) images() kubernetes.Images {
	return kubernetes.Images{
		KubeAPIServer: i.APIServer, KubeControllerManager: i.ControllerManager, KubeScheduler: i.Scheduler,
		KubeProxy: i.Proxy, Etcd: i.Etcd, CoreDNS: i.CoreDNS, Pause: i.Pause, Flannel: i.Flannel,
	}
}

// flannelSpec is the Flannel document's, its ranges decoded as clusterSpec's are.
type flannelSpec struct {
	MTU                *int           `json:"mtu"`
	VXLANSourceSubnets []netip.Prefix `json:"vxlanSourceSubnets"`
}

type addonSpec struct {
	Objects []map[string]any `json:"objects"`
}
