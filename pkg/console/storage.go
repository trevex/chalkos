package console

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// storage is the Storage tab: the disks, and the volumes with their encryption and use; in
// maintenance mode and on the installer, the disks as chalkctl disks lists them.
type storage struct {
	node NodeMsg
	pane Pane
}

func newStorage() *storage { return &storage{} }

func (s *storage) Title() string                     { return "Storage" }
func (s *storage) Applies(*nodev1.InfoResponse) bool { return true }
func (s *storage) Init(Client) tea.Cmd               { return nil }

func (s *storage) Update(msg tea.Msg) (Tab, tea.Cmd) {
	switch msg := msg.(type) {
	case NodeMsg:
		s.node = msg
	case tea.KeyMsg:
		s.pane.Key(msg)
	}
	return s, nil
}

func (s *storage) View(width, height int) string {
	if s.node.Info.Value == nil {
		return "Reading the disks..."
	}
	var b strings.Builder
	disks, status := s.node.Disks, s.node.Status
	if p := disks.Problem("disks"); p != "" {
		b.WriteString(ErrorStyle.Render(p) + "\n")
	}
	if !s.node.Normal() {
		if disks.Value != nil {
			b.WriteString("Disks, as chalkctl disks lists them:\n")
			var text strings.Builder
			if err := statusview.WriteDisks(&text, disks.Value.Disks); err != nil {
				fmt.Fprintln(&text, err)
			}
			b.WriteString(Clean(text.String()))
		}
		return s.pane.View(b.String(), width, height)
	}
	if disks.Value != nil {
		// The partitions and the WWN do not fit 80 columns; chalkctl disks lists them.
		t := statusview.Table(&b)
		fmt.Fprintln(t, "DISK\tSIZE\tTYPE\tMODEL\tSERIAL\tUSE")
		for _, d := range disks.Value.Disks {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", OneLine(d.Device), statusview.Size(d.Size), OneLine(d.Type), OneLine(d.Model), OneLine(d.Serial), OneLine(d.Usage))
		}
		t.Flush()
	}
	if p := status.Problem("status"); p != "" {
		b.WriteString(ErrorStyle.Render(p) + "\n")
	}
	if st := status.Value; st != nil {
		t := statusview.Table(&b)
		fmt.Fprintln(t, "VOLUME\tDISK\tMOUNT POINT\tENCRYPTION\tUSE")
		for _, v := range st.Volumes {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", OneLine(v.Name), OneLine(v.Disk), OneLine(v.MountPoint), OneLine(v.Encryption), volumeUse(v))
		}
		t.Flush()
		for _, d := range st.Disks {
			if d.Error != "" {
				b.WriteString(ErrorStyle.Render(OneLine(fmt.Sprintf("disk %s: %s", d.Name, d.Error))) + "\n")
			}
		}
	}
	return s.pane.View(b.String(), width, height)
}

// volumeUse is how much of a mounted volume is used, such as 1.2G of 9.8G (12%), or else the
// volume's state.
func volumeUse(v *nodev1.VolumeStatus) string {
	if v.SizeBytes == 0 {
		return statusview.VolumeState(v)
	}
	return fmt.Sprintf("%s of %s (%d%%)", statusview.Size(v.UsedBytes), statusview.Size(v.SizeBytes), v.UsedBytes*100/v.SizeBytes)
}
