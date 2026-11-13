package e2e

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/pki"
)

// TestImageBootsWithoutSecureBoot boots the unsigned image with Secure Boot off and installs it
// in place, then checks the store comes from dm-verity, the root is tmpfs, /etc is read-only,
// first boot added an empty slot B and a TPM-sealed STATE, and the install a TPM-sealed VAR. It
// then boots the node without its serial port and its TPM: STATE asks for the recovery key on
// the screen, which the keyboard answers, and the console on tty1 answers a password request of
// the booted system.
func TestImageBootsWithoutSecureBoot(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_IMAGE_DIR", "CHALKLAB_CONSOLE_FONT"}, chalkdEnv...)...)
	dir := vmDir(t)
	disk := prepareDisk(t, dir, diskOpts{})
	n := startNode(t, lab.VMConfig{Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS"), Disks: []lab.VMDisk{disk}})

	// Maintenance mode: STATE exists, VAR does not.
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"state_fstype": "ext4",
		"state_tpm2":   "1",
		"state_boots":  "1",
	})
	installInPlace(t, n, "chalklab")

	facts := readFacts(t, n.vm, 5*time.Minute)
	assertFacts(t, facts, map[string]string{
		"root_fstype":  "tmpfs",
		"usr_verity":   "verified",
		"etc_ro":       "1",
		"secureboot":   "0",
		"slot_b_empty": "2",
		"state_fstype": "ext4",
		"var_fstype":   "ext4",
		"state_tpm2":   "1",
		"var_tpm2":     "1",
		"state_boots":  "2",
		"var_boots":    "1",
	})

	// Without its serial port and its TPM, as a machine with a new TPM, the node boots with the
	// metal platform's consoles: the screen is last. STATE asks for the recovery key there, and
	// the keyboard answers; the VM has no other way in. The booted system then asks for a word,
	// which the console on tty1 answers.
	n.vm.Stop()
	c := n.vm.Config
	c.TPM, c.NoSerialPort = false, true
	c.Credentials = map[string]string{"chalktest.ask": "1"}
	n.vm = startVMConfig(t, c)
	font := consoleFont(t)
	unlockOnScreen(t, n, "chalklab", font, recoveryKey(t, "chalklab"))
	waitForNode(t, n, "chalklab")
	waitForScreen(t, n.vm, font, regexp.MustCompile(`node chalklab\s+normal`), 2*time.Minute)
	waitForScreen(t, n.vm, font, regexp.MustCompile(`(?s)systemd asks for a password.*chalktest asks for a word`), 2*time.Minute)
	if err := n.vm.Type("chalktest\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Minute, "chalktest-ask to log the answer", func() error {
		out, err := chalkctl(t, n, "base", "logs", "chalklab", "--unit", "chalktest-ask.service")
		if err != nil {
			return err
		}
		if !strings.Contains(out, "answer chalktest") {
			return fmt.Errorf("its journal:\n%s", out)
		}
		return nil
	})
}

// passphrasePromptRE is systemd-cryptsetup's question on a console: what it asks for, a
// passphrase, a recovery key or either, and the volume.
var passphrasePromptRE = regexp.MustCompile(`Please enter [a-z ]+ for disk ([^\s:]+)`)

// unlockOnScreen types key at each passphrase prompt the screen shows until the installed node
// answers. STATE asks first; VAR asks too unless systemd-cryptsetup takes the key it cached. The
// prompt shows a star or a bullet for each key typed, which shows the keyboard reaches it.
func unlockOnScreen(t *testing.T, n *node, name string, font *lab.Font, key string) {
	t.Helper()
	cert, err := issueClientCertificate(pki.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	answered := map[string]bool{}
	waitForScreen(t, n.vm, font, passphrasePromptRE, 5*time.Minute)
	err = poll(10*time.Minute, 2*time.Second, func() error {
		if len(answered) > 0 {
			if c, err := dialInstalled(n, name, cert); err == nil && info(c) == nil {
				return nil
			}
		}
		text, err := screenText(n.vm, font)
		if err != nil {
			return err
		}
		all := passphrasePromptRE.FindAllStringSubmatch(text, -1)
		if len(all) == 0 || answered[all[len(all)-1][1]] {
			return errors.New("the node does not answer and no new prompt shows")
		}
		disk := all[len(all)-1][1]
		if err := n.vm.Type(key); err != nil {
			return err
		}
		waitForScreen(t, n.vm, font, regexp.MustCompile(`for disk `+regexp.QuoteMeta(disk)+`.*[*•]{8}`), time.Minute)
		if err := n.vm.Type("\n"); err != nil {
			return err
		}
		answered[disk] = true
		return errors.New("typed the key for " + disk)
	})
	if err != nil {
		text, _ := screenText(n.vm, font)
		t.Fatalf("unlocking %s on the screen: %v; the screen:\n%s", name, err, text)
	}
}

// recoveryKey is the node's recovery key, from the test secrets, as chalkctl recovery-key prints
// it.
func recoveryKey(t *testing.T, node string) string {
	t.Helper()
	s, err := readSecrets()
	if err != nil {
		t.Fatal(err)
	}
	key, err := clustersecrets.RecoveryKey(s.RecoverySecret, node)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
