package chalkd

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// bootStatus reads the boot from the ESP: the entry the node booted and whether it was blessed,
// and the other UKI of its image an upgrade installed, staged to boot next or failed. A UKI whose
// tries are used up while the node runs another image is a version it fell back from.
func (s *Server) bootStatus() *nodev1.BootStatus {
	st := &nodev1.BootStatus{Version: readOSRelease(s.Paths.OSRelease)["IMAGE_VERSION"]}
	cmdline, err := os.ReadFile(s.Paths.Cmdline)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	running, err := uki.UsrHash(string(cmdline))
	if err != nil {
		st.Error = "the running store: " + err.Error()
		return st
	}
	st.RootHash = running
	entries, err := upgrade.Entries(s.Paths.ESP)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	booted, err := upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Entry, st.Blessed = booted.File, booted.TriesLeft < 0
	var failed upgrade.Entry
	for _, e := range entries {
		switch {
		// UKIs of other images on the ESP, such as a rescue system's, are no upgrades of the node.
		case e.ImageID != booted.ImageID, e.ID == booted.ID, bytes.Equal(e.RootHash, running):
		case e.Bad():
			failed = e
		case e.TriesLeft > 0:
			st.Staged = e.Version
		}
	}
	if failed.Version != "" {
		st.Failed = failed.Version
		st.Journal = s.failedBoot(failed)
	}
	return st
}

// failedBoot returns what chalkd and the health check logged during the last boot of the UKI
// that was not found healthy, as the check recorded it on VAR; nil without a record of a boot of
// that version and store.
func (s *Server) failedBoot(e upgrade.Entry) []string {
	data, err := os.ReadFile(s.Paths.FailedBoot)
	if err != nil || len(e.RootHash) == 0 {
		return nil
	}
	recorded, lines, _ := strings.Cut(string(data), "\n")
	if recorded != recordHeader(e.Version, e.RootHash) {
		return nil
	}
	return strings.Split(strings.TrimRight(lines, "\n"), "\n")
}

// recordHeader is the first line of the record of a failed boot: the version and the root hash
// of the store it booted, so a record is never shown for another build of the version.
func recordHeader(version string, rootHash []byte) string {
	return "version " + version + " usrhash=" + hex.EncodeToString(rootHash)
}
