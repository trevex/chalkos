package statusview

import (
	"strconv"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// The values of a Comparison's fields.
const (
	Same    = "same"
	Differs = "differs"
	Unknown = "unknown"
)

// Comparison is how a node compares with the inventory, as chalkctl status -o json prints it:
// each field is same, differs or unknown.
type Comparison struct {
	// Config compares the config of the running image's slot with the inventory's.
	Config string `json:"config"`
	// StagedConfig compares the config of the staged image's slot; empty without a staged image.
	StagedConfig string `json:"stagedConfig,omitempty"`
	// Image compares the store the node runs with the image the inventory names for it.
	Image string `json:"image"`
	// Platform compares the platform the node runs on with its image's.
	Platform string `json:"platform"`
}

// Problems are what the status s of a node shows wrong, each in a few words: the sections the node
// could not read, and then, in the order Write prints them, the config's trial and activation, the
// boot, the inventory's comparisons, the module policy, volumes, disks, outputs, certificates, the
// clock and failed units. None is a healthy node.
func Problems(s *nodev1.StatusResponse, o Options) []string {
	var p []string
	for _, e := range s.Errors {
		p = append(p, e.Section+" unknown")
	}
	if a := s.GetConfigActivation(); a != nil {
		switch a.GetState() {
		case nodev1.ActivationState_ACTIVATION_STATE_FAILED:
			p = append(p, "config activation failed")
		case nodev1.ActivationState_ACTIVATION_STATE_INTERRUPTED:
			p = append(p, "config activation interrupted")
		case nodev1.ActivationState_ACTIVATION_STATE_UNKNOWN:
			p = append(p, "config activation unknown")
		}
	}
	if s.GetTrial().GetError() != "" {
		p = append(p, "trial rollback failed")
	}
	if s.TrialError != "" {
		p = append(p, "trial record unreadable")
	}
	var c Comparison
	if o.Inventory != nil {
		c = o.Inventory.Compare(s)
	}
	if c.Config == Differs {
		p = append(p, "config not the inventory's")
	}
	if c.StagedConfig == Differs {
		p = append(p, "staged config not the inventory's")
	}
	if c.Platform == Differs {
		p = append(p, "platform not the image's")
	}
	switch b := s.Boot; {
	case b.GetError() != "":
		p = append(p, "boot unknown")
	case b != nil && !b.Blessed:
		p = append(p, "boot not found healthy yet")
	}
	if f := s.Boot.GetFailed(); f != nil {
		p = append(p, "upgrade to "+BootImageName(f)+" failed")
	}
	if c.Image == Differs {
		p = append(p, "image not the inventory's")
	}
	switch o.ModulePolicy.GetState() {
	case nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED:
		p = append(p, "module policy failed")
	case nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED:
		p = append(p, "module policy tampered")
	}
	for _, v := range s.Volumes {
		if state := VolumeState(v); state == "missing" || state == "not mounted" {
			p = append(p, "volume "+v.Name+" "+state)
		}
	}
	for _, d := range s.Disks {
		if d.Error != "" {
			p = append(p, "disk "+d.Name+" not found")
		}
	}
	for _, out := range s.Outputs {
		if out.Error != "" {
			p = append(p, "output "+out.Name+" failed")
		}
	}
	for _, cert := range s.Certificates {
		if cert.Problem != "" {
			why, _, _ := strings.Cut(cert.Problem, ";")
			p = append(p, "certificate "+cert.Name+" "+why)
		}
	}
	switch t := s.Time; {
	case t.GetError() != "":
		p = append(p, "clock unknown")
	case t != nil && !t.Synchronised:
		p = append(p, "clock not synchronised")
	}
	for _, u := range s.FailedUnits {
		p = append(p, "unit "+u+" failed")
	}
	return p
}

// SummaryLine is the first line of a node's status: healthy, or the problems.
func SummaryLine(node string, problems []string) string {
	switch len(problems) {
	case 0:
		return node + ": healthy"
	case 1:
		return node + ": 1 problem: " + problems[0]
	}
	return node + ": " + strconv.Itoa(len(problems)) + " problems: " + strings.Join(problems, ", ")
}
