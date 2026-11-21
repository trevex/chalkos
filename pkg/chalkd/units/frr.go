package units

import (
	"errors"
	"io/fs"
	"os"

	"github.com/trevex/chalkos/pkg/frr/credentials"
)

// frrConfig is the config frr.service fills in, which /etc/frr/frr.conf links to and the daemons
// load.
const frrConfig = "/run/frr/frr.conf"

// FRRCredentials writes out from the rendered config, or from fallback while there is none, as on
// a node in maintenance mode, with the peers' passwords filled in from the credentials directory
// dir, as credentials.Fill does.
func FRRCredentials(rendered, fallback, out, dir string) error {
	src := rendered
	if _, err := os.Stat(rendered); errors.Is(err, fs.ErrNotExist) {
		src = fallback
	}
	return credentials.Fill(src, out, dir)
}
