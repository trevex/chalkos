// Package constants holds the values that chalkos's Nix library and its Go code share, from
// constants.json, which lib/kinds/api.nix reads too: the API groups of chalkos's kinds and their
// version, networkd's kinds, the patterns of names and the normalisers of uniqueness declarations.
package constants

import (
	_ "embed"
	"regexp"

	"github.com/trevex/chalkos/pkg/jsonfile"
)

//go:embed constants.json
var data []byte

type file struct {
	Version       string            `json:"version"`
	Groups        map[string]string `json:"groups"`
	NetworkdKinds []string          `json:"networkdKinds"`
	DocumentName  string            `json:"documentName"`
	PlainName     string            `json:"plainName"`
	Normalisers   []string          `json:"normalisers"`
}

var shared = parse(data)

func parse(data []byte) file {
	var f file
	if err := jsonfile.DecodeStrict(data, &f); err != nil {
		panic("constants.json: " + err.Error())
	}
	return f
}

var (
	// Version is the version of chalkos's API groups.
	Version = shared.Version
	// Groups are chalkos's API groups by a short name of their own, such as core for chalkos.dev.
	Groups = shared.Groups
	// NetworkdKinds are networkd's kinds, whose schemas come from the image's systemd.
	NetworkdKinds = shared.NetworkdKinds
	// DocumentName is a document's metadata.name, as every kind's schema holds it to the pattern.
	DocumentName = anchored(shared.DocumentName)
	// PlainName is the name of an output or a validator, which names files and directories on
	// the node and appears in commands: lower-case letters, digits and dashes.
	PlainName = anchored(shared.PlainName)
	// Normalisers are the names of the normalisers a uniqueness declaration may compare its values
	// with, which pkg/config implements.
	Normalisers = shared.Normalisers
)

// APIVersion is the apiVersion of the group of chalkos's that Groups names group, such as
// chalkos.dev/v1alpha1 for core. It panics for a name Groups lacks.
func APIVersion(group string) string {
	g, ok := Groups[group]
	if !ok {
		panic("constants.json names no group " + group)
	}
	return g + "/" + Version
}

// anchored compiles pattern, as Nix's builtins.match reads it, to match a whole string.
func anchored(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`^(?:` + pattern + `)$`)
}
