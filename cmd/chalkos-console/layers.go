package main

import (
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/console"
)

// layerTabs are the tabs of the layers compiled into the console, in the order of their names,
// which reach the layers' APIs over conn. A tab shows on images with its layer.
func layerTabs(conn *client.Conn) []console.Tab { return nil }
