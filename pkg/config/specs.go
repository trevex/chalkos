package config

import (
	"github.com/trevex/chalkos/pkg/constants"
	"github.com/trevex/chalkos/pkg/storage"
)

// API versions of the kinds chalkos declares itself.
var (
	CoreAPI     = constants.APIVersion("core")
	StorageAPI  = constants.APIVersion("storage")
	TimeAPI     = constants.APIVersion("time")
	NetworkdAPI = constants.APIVersion("networkd")
	OCIAPI      = constants.APIVersion("oci")
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
	Exec       bool             `json:"exec"`
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
