// Package sdk is what chalkctl and other clients of chalkd share: admin credentials from a
// cluster's secrets, connections to nodes in maintenance mode and in normal mode, the contract of
// an image, and the check of a node's config against it, as the node would check it.
//
// A layer takes part through a Layer, which a caller passes to the functions that ask layers; the
// package names no layer itself. A Layer adds what it does through optional interfaces: the facts
// its images carry, the validators it links, the documents a node gets and the payloads it issues.
// The layers' own parts live in pkg/sdk/layers/<layer>.
package sdk
