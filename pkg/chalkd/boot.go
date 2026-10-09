package chalkd

import (
	"bytes"
	"os"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// bootStatus reads the boot from the ESP: the entry the node booted and whether it was blessed,
// and the other UKI an upgrade installed, staged to boot next or failed. A UKI whose tries are
// used up while the node runs another image is a version it fell back from.
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
	for _, e := range entries {
		switch {
		case e.ID == booted.ID || bytes.Equal(e.RootHash, running):
		case e.Bad():
			st.Failed = e.Version
		case e.TriesLeft > 0:
			st.Staged = e.Version
		}
	}
	if st.Failed != "" {
		st.Journal = s.failedBoot(st.Failed)
	}
	return st
}

// failedBoot returns what chalkd and the health check logged during the last boot of the version
// that was not found healthy, as the check recorded it on VAR; nil without a record of it.
func (s *Server) failedBoot(version string) []string {
	data, err := os.ReadFile(s.Paths.FailedBoot)
	if err != nil {
		return nil
	}
	recorded, lines, _ := strings.Cut(string(data), "\n")
	if recorded != "version "+version {
		return nil
	}
	return strings.Split(strings.TrimRight(lines, "\n"), "\n")
}
