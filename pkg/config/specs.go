package config

import "github.com/trevex/chalkos/pkg/storage"

// API versions and kinds chalkos declares itself.
const (
	CoreAPI     = "chalkos.dev/v1alpha1"
	StorageAPI  = "storage.chalkos.dev/v1alpha1"
	TimeAPI     = "time.chalkos.dev/v1alpha1"
	NetworkdAPI = "networkd.chalkos.dev/v1alpha1"
	OCIAPI      = "oci.chalkos.dev/v1alpha1"
)

// SecretRef names a key of a Secret document.
type SecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type hostSpec struct {
	Hostname *string `json:"hostname"`
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

type mirrorSpec struct {
	Registry  string   `json:"registry"`
	Endpoints []string `json:"endpoints"`
}

type registrySpec struct {
	Host     string  `json:"host"`
	Insecure bool    `json:"insecure"`
	Username *string `json:"username"`
	Password *struct {
		SecretRef SecretRef `json:"secretRef"`
	} `json:"password"`
}
