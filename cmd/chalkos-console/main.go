// Command chalkos-console is the console on a machine's screen: on tty1, in place of a login
// prompt, it shows the node that chalkd serves on its console socket. It takes no arguments;
// CHALKOS_CONSOLE_ACTIONS=1 in its unit's environment offers the actions, which chalkd allows
// only as the image does.
package main

import (
	"log"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/console"
)

// socket is chalkd's console socket.
var socket = filepath.Join("/run/chalkd", "console.sock")

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkos-console: ")
	if len(os.Args) > 1 {
		log.Fatal("chalkos-console takes no arguments")
	}
	conn := client.DialSocket(socket)
	defer conn.Close()
	m := console.New(console.NewClient(conn), console.Options{
		Tabs:    layerTabs(conn),
		Actions: os.Getenv("CHALKOS_CONSOLE_ACTIONS") == "1",
	})
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		log.Fatal(err)
	}
}
