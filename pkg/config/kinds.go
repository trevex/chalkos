// Package config reads a node's config documents, checks them as a set against the schemas of
// the image they are meant for, and translates them into the node's identity.
package config

import (
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/storage"
)

// API versions and kinds chalkos declares itself.
const (
	CoreAPI       = "chalkos.dev/v1alpha1"
	StorageAPI    = "storage.chalkos.dev/v1alpha1"
	TimeAPI       = "time.chalkos.dev/v1alpha1"
	KubernetesAPI = "kubernetes.chalkos.dev/v1alpha1"
	NetworkdAPI   = "networkd.chalkos.dev/v1alpha1"
)

// SecretRef names a key of a Secret document.
type SecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type hostSpec struct {
	Hostname *string `json:"hostname"`
}

type secretSpec struct {
	Data map[string]string `json:"data"`
}

type systemDiskSpec struct {
	Disk       storage.DiskRef `json:"disk"`
	Encryption struct {
		Mode     string `json:"mode"`
		Fallback string `json:"fallback"`
	} `json:"encryption"`
	Var struct {
		Size       *string `json:"size"`
		Encryption struct {
			Mode *string `json:"mode"`
		} `json:"encryption"`
	} `json:"var"`
}

type volumeSpec struct {
	Disk       *storage.DiskRef `json:"disk"`
	Size       *string          `json:"size"`
	Format     string           `json:"format"`
	MountPoint *string          `json:"mountPoint"`
	Encryption struct {
		Mode *string `json:"mode"`
	} `json:"encryption"`
	Repart map[string]string `json:"repart"`
}

type timeSyncSpec struct {
	Servers []struct {
		Host string `json:"host"`
		NTS  bool   `json:"nts"`
		Port *int   `json:"port"`
	} `json:"servers"`
	DHCP bool `json:"dhcp"`
}

// nodeSpec is the Kubernetes Node's.
type nodeSpec struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
	Taints []struct {
		Key    string  `json:"key"`
		Value  *string `json:"value"`
		Effect string  `json:"effect"`
	} `json:"taints"`
	// NodeIP is how the node gets its addresses: nil Addresses take the static ones, nil
	// ValidSubnets the Cluster's.
	NodeIP struct {
		Addresses    []string `json:"addresses"`
		ValidSubnets []string `json:"validSubnets"`
	} `json:"nodeIP"`
}

type clusterSpec struct {
	Endpoint                       string                       `json:"endpoint"`
	Domain                         string                       `json:"domain"`
	AllowSchedulingOnControlPlanes bool                         `json:"allowSchedulingOnControlPlanes"`
	IPFamilies                     []string                     `json:"ipFamilies"`
	PodCIDRs                       kubernetes.ByFamily[string]  `json:"podCIDRs"`
	ServiceCIDRs                   kubernetes.ByFamily[string]  `json:"serviceCIDRs"`
	DNSIPs                         kubernetes.ByFamily[*string] `json:"dnsIPs"`
	NodeCIDRMaskSizes              kubernetes.ByFamily[int]     `json:"nodeCIDRMaskSizes"`
	NodeIP                         kubernetes.NodeIP            `json:"nodeIP"`
	VXLANSourceSubnets             []string                     `json:"vxlanSourceSubnets"`
	VIP                            struct {
		Addresses []string `json:"addresses"`
		Mode      string   `json:"mode"`
		Interface *string  `json:"interface"`
	} `json:"vip"`
	ExtraArgs map[string]map[string]string `json:"extraArgs"`
}

type flannelSpec struct {
	MTU *int `json:"mtu"`
}

type registryMirrorSpec struct {
	Registry       string   `json:"registry"`
	Mirrors        []string `json:"mirrors"`
	AllowPlainHTTP bool     `json:"allowPlainHTTP"`
}

type addonSpec struct {
	Objects []map[string]any `json:"objects"`
}
