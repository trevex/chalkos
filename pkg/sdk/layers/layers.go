// Package layers names every layer's part in the SDK, for a client that knows them all, such as a
// Cluster API provider of any image. A client that knows some layers alone passes those.
package layers

import (
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/sdk/layers/router"
)

// All are the SDK's layers, in the order of their names.
func All() []sdk.Layer { return []sdk.Layer{kubernetes.Layer{}, router.Layer{}} }
