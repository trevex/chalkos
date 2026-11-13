package console_test

import (
	"testing"

	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
)

// The Storage tab shows the disks and the volumes with their encryption and use.
func TestStorage(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Normal(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "healthy")
	consoletest.Press(tm, "3")
	consoletest.WaitFor(t, tm, "ENCRYPTION")
	consoletest.Snapshot(t, tm)
}

// In maintenance mode the Storage tab lists the disks as chalkctl disks does, to pick one to
// install onto.
func TestStorageMaintenance(t *testing.T) {
	tm := consoletest.Start(t, console.New(consoletest.Maintenance(), consoletest.Options()))
	consoletest.WaitFor(t, tm, "maintenance mode")
	consoletest.Press(tm, "3")
	consoletest.WaitFor(t, tm, "DEVICE")
	consoletest.Snapshot(t, tm)
}
