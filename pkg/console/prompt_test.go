package console_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest"

	"github.com/trevex/chalkos/pkg/askpassword"
	"github.com/trevex/chalkos/pkg/askpassword/askpasswordtest"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// promptOptions are the test options with the requests of dir and a clock the test moves on;
// the console's actions stay off, as answering does not need them.
func promptOptions(dir string) (console.Options, *consoletest.Clock) {
	o := consoletest.Options()
	o.Asks = askpassword.Agent{Dir: dir}
	clock := consoletest.NewClock()
	o.Now = clock.Now
	return o, clock
}

// startPrompt runs the console of a normal node with the requests of dir.
func startPrompt(t *testing.T, dir string) (*teatest.TestModel, *consoletest.Clock) {
	t.Helper()
	o, clock := promptOptions(dir)
	return consoletest.Start(t, console.New(consoletest.Normal(), o)), clock
}

// enter presses Enter a second later, past the time a new prompt ignores it.
func enter(tm *teatest.TestModel, clock *consoletest.Clock) {
	clock.Advance(time.Second)
	consoletest.Press(tm, "enter")
}

var volume = askpasswordtest.Request{Name: "data", Message: "Please enter passphrase for disk data:", ID: "cryptsetup:/dev/vdb1"}

// A request takes the screen below the header: its question, where a volume's key comes from,
// and a star for each character typed.
func TestPrompt(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, volume)
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "abc")
	consoletest.WaitFor(t, tm, "> ***_")
	consoletest.Snapshot(t, tm)
}

// Enter sends the password; the prompt goes when the request does, and the tabs come back. a,
// which opens the actions elsewhere, is a character of the password here, with the actions on.
func TestPromptAnswers(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	o, clock := promptOptions(dir)
	o.Actions = true
	tm := consoletest.Start(t, console.New(consoletest.Normal(), o))
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "a s")
	consoletest.Press(tm, "backspace")
	consoletest.Press(tm, "3")
	enter(tm, clock)
	if got := askpasswordtest.Next(t, answers); got != "+a 3" {
		t.Errorf("answer %q, want +a 3", got)
	}
	askpasswordtest.Remove(t, dir, volume.Name)
	consoletest.WaitFor(t, tm, "1 Overview")
}

// Enter right after a prompt comes on screen, as meant for what was there before, sends nothing.
func TestPromptIgnoresAnEarlyEnter(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "enter")
	consoletest.Press(tm, "k")
	consoletest.WaitFor(t, tm, "> *_")
	select {
	case got := <-answers:
		t.Fatalf("an early Enter sent %q", got)
	default:
	}
	enter(tm, clock)
	if got := askpasswordtest.Next(t, answers); got != "+k" {
		t.Errorf("answer %q, want +k", got)
	}
}

// Ctrl+U erases what was typed.
func TestPromptErasesTheLine(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "wrong")
	consoletest.Press(tm, "ctrl+u")
	consoletest.Press(tm, "right")
	enter(tm, clock)
	if got := askpasswordtest.Next(t, answers); got != "+right" {
		t.Errorf("answer %q, want +right", got)
	}
}

// The prompt takes no more than the asker reads: a character that would pass
// askpassword.MaxPassword bytes is refused, a shorter one still fits.
func TestPromptCapsTheInput(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, strings.Repeat("x", askpassword.MaxPassword-1))
	consoletest.Press(tm, "é")
	consoletest.Press(tm, "yz")
	enter(tm, clock)
	if got, want := askpasswordtest.Next(t, answers), "+"+strings.Repeat("x", askpassword.MaxPassword-1)+"y"; got != want {
		t.Errorf("answer of %d bytes ending %q, want %d bytes ending in y", len(got), got[max(0, len(got)-3):], len(want))
	}
}

// Esc cancels the request.
func TestPromptCancels(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "esc")
	if got := askpasswordtest.Next(t, answers); got != "-" {
		t.Errorf("answer %q, want -", got)
	}
}

// A request that asks for echo shows what is typed.
func TestPromptEcho(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "user", Message: "User name:", Echo: true})
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "User name:")
	consoletest.Press(tm, "admin")
	consoletest.WaitFor(t, tm, "> admin_")
	consoletest.Snapshot(t, tm)
}

// A silent request shows nothing of what is typed, not even stars.
func TestPromptSilent(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "pin", Message: "PIN:", Silent: true})
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "PIN:")
	consoletest.Press(tm, "1234")
	if screen := finalScreen(t, tm); !slices.Contains(strings.Split(screen, "\n"), "> _") {
		t.Errorf("the prompt shows what was typed:\n%s", screen)
	}
}

// longQuestion is a question longer than the screen.
var longQuestion = askpasswordtest.Request{Name: "long", Message: strings.Repeat("Please enter the passphrase. ", 80), ID: "cryptsetup:/dev/vdb1"}

// A question longer than the screen is cut, so the input stays on screen.
func TestPromptLongQuestion(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, longQuestion)
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "…")
	consoletest.Press(tm, "ab")
	consoletest.WaitFor(t, tm, "> **_")
	consoletest.Snapshot(t, tm)
}

// In maintenance mode the header is taller, and the question is cut shorter.
func TestPromptLongQuestionMaintenance(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, longQuestion)
	o, _ := promptOptions(dir)
	tm := consoletest.Start(t, console.New(consoletest.Maintenance(), o))
	consoletest.WaitFor(t, tm, "2001:db8::50")
	consoletest.Press(tm, "ab")
	consoletest.WaitFor(t, tm, "> **_")
	consoletest.Snapshot(t, tm)
}

// A request that goes before an answer, as another agent answered it or it timed out, closes the
// prompt.
func TestPromptClosesWhenTheRequestGoes(t *testing.T) {
	dir := t.TempDir()
	askpasswordtest.Ask(t, dir, volume)
	tm, _ := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "x")
	askpasswordtest.Remove(t, dir, volume.Name)
	consoletest.WaitFor(t, tm, "1 Overview")
}

// A request whose file stays after its answer, as the asker did not take it, comes back.
func TestPromptComesBackWhenTheAnswerIsNotTaken(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, volume)
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
	consoletest.Press(tm, "x")
	enter(tm, clock)
	askpasswordtest.Next(t, answers)
	consoletest.WaitFor(t, tm, "1 Overview")
	clock.Advance(10 * time.Second)
	// A change in the directory makes the console read the requests again.
	if err := os.WriteFile(filepath.Join(dir, "other"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
}

// Two requests are answered one after the other, the older first, the second as soon as the
// first is answered.
func TestPromptTwoRequestsInOrder(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	second := askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "b", Message: "Second question:", Since: base.Add(time.Minute)})
	first := askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "a", Message: "First question:", Since: base})
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "First question:", "1 more request waits")
	consoletest.Press(tm, "one")
	enter(tm, clock)
	if got := askpasswordtest.Next(t, first); got != "+one" {
		t.Errorf("first answer %q", got)
	}
	consoletest.WaitFor(t, tm, "Second question:")
	consoletest.Press(tm, "two")
	enter(tm, clock)
	if got := askpasswordtest.Next(t, second); got != "+two" {
		t.Errorf("second answer %q", got)
	}
}

// An answer that does not reach its request says so, and the request comes back.
func TestPromptAnswerFails(t *testing.T) {
	dir := t.TempDir()
	data := "[Ask]\nSocket=" + filepath.Join(dir, "sck.gone") + "\nMessage=Lost question:\n"
	if err := os.WriteFile(filepath.Join(dir, "ask.gone"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	tm, clock := startPrompt(t, dir)
	consoletest.WaitFor(t, tm, "Lost question:")
	consoletest.Press(tm, "x")
	enter(tm, clock)
	consoletest.WaitFor(t, tm, "the answer did not reach the request")
	if screen := finalScreen(t, tm); !strings.Contains(screen, "Lost question:") {
		t.Errorf("the request is not on screen again:\n%s", screen)
	}
}

// The prompt shows once the requests' directory is made, after the console started.
func TestPromptAfterTheDirectoryIsMade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ask-password")
	o, _ := promptOptions(dir)
	o.Interval = 50 * time.Millisecond
	tm := consoletest.Start(t, console.New(consoletest.Normal(), o))
	consoletest.WaitFor(t, tm, "healthy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	askpasswordtest.Ask(t, dir, volume)
	consoletest.WaitFor(t, tm, "Please enter passphrase for disk data:")
}

// finalScreen ends the console and returns its last screen.
func finalScreen(t *testing.T, tm *teatest.TestModel) string {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	return tm.FinalModel(t, teatest.WithFinalTimeout(20*time.Second)).View()
}
