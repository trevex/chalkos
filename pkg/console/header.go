package console

import (
	"bytes"
	"cmp"
	"fmt"
	"strings"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// headerLines are the header's rows: who the node is, which image it runs, its health, a
// passphrase prompt that waits, and in maintenance mode and on the installer the certificate's
// fingerprint and the addresses that install needs.
func (m *Model) headerLines() []string {
	info := m.node.Info.Value
	first := []string{TitleStyle.Render(OneLine(cmp.Or(info.GetHostname(), "chalkos")))}
	if n := info.GetNodeName(); n != "" {
		first = append(first, "node "+OneLine(n))
	}
	if w := modeWord(info); w != "" {
		first = append(first, w)
	}
	if up, err := m.o.Uptime(); err == nil {
		first = append(first, "up "+uptimeWords(up))
	}
	second := []string{"image " + statusview.ImageName(info.GetRootHash(), "", "")}
	if g := m.node.Status.Value.GetBoot().GetRunning().GetGeneration(); g > 0 {
		second[0] += fmt.Sprintf(" generation %d", g)
	}
	if v := info.GetVariant(); v != "" {
		second = append(second, OneLine(v+" on "+cmp.Or(info.GetPlatform(), "every platform")))
	}
	lines := []string{strings.Join(first, "  "), strings.Join(second, "  "), m.healthLine()}
	if m.prompt {
		name := OneLine(cmp.Or(info.GetNodeName(), "<node>"))
		for _, row := range wrapWords("passphrase", "prompt waits on this screen; chalkctl recovery-key "+name+" prints the key", m.width) {
			lines = append(lines, WarnStyle.Render(row))
		}
	}
	if info != nil && !m.node.Normal() {
		lines = append(lines, wrapChars("fingerprint", OneLine(info.GetFingerprint()), m.width)...)
		lines = append(lines, wrapWords("addresses", m.addressList(), m.width)...)
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return lines
}

// modeWord names the node's mode: normal, maintenance, or installer; empty before Info comes.
func modeWord(info *nodev1.InfoResponse) string {
	switch {
	case info.GetInstaller():
		return "installer"
	case info.GetMode() == nodev1.Mode_MODE_NORMAL:
		return "normal"
	case info.GetMode() == nodev1.Mode_MODE_MAINTENANCE:
		return "maintenance"
	}
	return ""
}

// uptimeWords is a time the machine has run in its two largest units, such as 3d 4h or 4h 12m.
func uptimeWords(d time.Duration) string {
	days, hours := int(d/(24*time.Hour)), int(d/time.Hour)%24
	minutes, seconds := int(d/time.Minute)%60, int(d/time.Second)%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}

// healthLine is the header's row of the node's health: whether chalkd answers, the failed units,
// a config on trial and a rollback that boots next, or else that all is well.
func (m *Model) healthLine() string {
	if err := m.node.Info.Err; err != nil {
		return ErrorStyle.Render(OneLine(fmt.Sprintf("chalkd does not answer, retrying: %v", err)))
	}
	info := m.node.Info.Value
	switch {
	case info == nil:
		return "reading the node..."
	case info.GetInstaller():
		return "installer: chalkctl install installs nodes from here"
	case !m.node.Normal():
		return "maintenance mode: chalkctl install installs this node"
	}
	st := m.node.Status.Value
	if st == nil {
		if p := m.node.Status.Problem("status"); p != "" {
			return ErrorStyle.Render(p)
		}
		return "reading the node's status..."
	}
	var parts []string
	if n := len(st.FailedUnits); n > 0 {
		parts = append(parts, ErrorStyle.Render(OneLine(count(n, "failed unit")+": "+strings.Join(st.FailedUnits, ", "))))
	}
	if tr := st.Trial; tr != nil {
		parts = append(parts, WarnStyle.Render(OneLine(fmt.Sprintf("config %s on trial until %s", tr.Version[:min(12, len(tr.Version))], tr.Deadline.AsTime().Local().Format("15:04")))))
	}
	if st.TrialError != "" {
		parts = append(parts, ErrorStyle.Render("the record of a config's trial cannot be read"))
	}
	if b := st.Boot; b.GetStaged() != nil && bytes.Equal(b.GetStaged().GetRootHash(), b.GetRollback().GetRootHash()) {
		parts = append(parts, WarnStyle.Render(OneLine("rollback to "+statusview.BootImageName(b.Staged)+" boots next")))
	}
	if len(parts) == 0 {
		return OKStyle.Render("healthy")
	}
	return strings.Join(parts, "; ")
}

// addressList is the node's addresses beyond link-local, or why they cannot be read.
func (m *Model) addressList() string {
	switch {
	case m.addresses.Err != nil && m.addresses.At.IsZero():
		return ErrorStyle.Render(m.addresses.Problem("addresses"))
	case m.addresses.At.IsZero():
		return "reading..."
	}
	list := OneLine(cmp.Or(strings.Join(m.addresses.Value, " "), "none"))
	if p := m.addresses.Problem("addresses"); p != "" {
		list += " " + ErrorStyle.Render(p)
	}
	return list
}
