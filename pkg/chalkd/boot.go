package chalkd

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// bootStatus reads the boot from the ESP: the entry the node booted and whether it was blessed,
// and the other UKI of its image an upgrade installed, staged to boot next or failed. A UKI whose
// tries are used up while the node runs another image is an image it fell back from. It also
// names the image a rollback boots, the one an upgrade replaced, while STATE keeps its config
// slot, and the image the last rollback rolled back to, while the node runs or stages it. Each
// image is named by its root hash and generation, with the provenance the record of installed
// images gives it.
func (s *Server) bootStatus() *nodev1.BootStatus {
	st := &nodev1.BootStatus{}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	images := s.images()
	st.Running = bootImage(images, running, 0)
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	booted, err := upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Running.Generation = booted.Generation
	st.Entry, st.Blessed = booted.File, booted.TriesLeft < 0
	var failed esp.Entry
	for _, e := range entries {
		switch {
		// UKIs of other images on the ESP, such as a rescue system's, are no upgrades of the node.
		case e.ImageID != booted.ImageID, e.ID == booted.ID, bytes.Equal(e.RootHash, running):
		case e.Bad():
			failed = e
		}
	}
	// The staged entry is the one that boots next, counted after an upgrade and not after a
	// rollback from a boot that was not found healthy; a counted entry below the booted one, as an
	// image the node rolled back from leaves it, does not boot next.
	if e, ok := esp.StagedEntry(entries, booted.ImageID, booted); ok {
		st.Staged = bootImage(images, e.RootHash, e.Generation)
	}
	if failed.File != "" {
		st.Failed = bootImage(images, failed.RootHash, failed.Generation)
		st.Journal = s.failedBoot(failed)
	}
	// While another image boots next, a rollback is refused.
	if prev, ok := upgrade.Previous(entries, booted.ImageID, booted, running); ok && st.Staged == nil && s.keepsSlot(prev.RootHash) {
		st.Previous = bootImage(images, prev.RootHash, prev.Generation)
	}
	if to := s.rolledBackTo(); to != nil {
		switch {
		case bytes.Equal(to, running):
			st.Rollback = bootImage(images, to, st.Running.Generation)
		case st.Staged != nil && bytes.Equal(to, st.Staged.RootHash):
			st.Rollback = bootImage(images, to, st.Staged.Generation)
		}
	}
	return st
}

// images is the node's record of the images it installed, which names their provenance.
func (s *Server) images() upgrade.Images {
	return upgrade.ReadImages(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord))
}

// bootImage is the image of the root hash, with its entry's generation and its provenance.
func bootImage(images upgrade.Images, rootHash []byte, generation uint64) *nodev1.BootImage {
	p := images.Of(rootHash)
	return &nodev1.BootImage{RootHash: rootHash, Generation: generation, Revision: p.Revision, Tag: p.Tag}
}

// failedBoot returns what chalkd and the health check logged during the last boot of the UKI
// that was not found healthy, as the check recorded it on VAR; nil without a record of a boot of
// that store and generation.
func (s *Server) failedBoot(e esp.Entry) []string {
	data, err := os.ReadFile(s.Paths.FailedBoot)
	if err != nil || len(e.RootHash) == 0 {
		return nil
	}
	recorded, lines, _ := strings.Cut(string(data), "\n")
	if recorded != recordHeader(e.RootHash, e.Generation) {
		return nil
	}
	return strings.Split(strings.TrimRight(lines, "\n"), "\n")
}

// recordHeader is the first line of the record of a failed boot: the root hash of the store it
// booted and the generation of its entry, so a record is never shown for another image, nor for
// another try of the image under a later generation.
func recordHeader(rootHash []byte, generation uint64) string {
	return "usrhash=" + hex.EncodeToString(rootHash) + " generation=" + strconv.FormatUint(generation, 10)
}
