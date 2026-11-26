package chalkctl

import (
	"context"
	"fmt"
	"io"
	"sync"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/client"
)

// releaseCheck warns when a command meets a chalkd of another release than chalkctl's own, once
// per release of chalkd: chalkctl and chalkd keep no compatibility between releases, so new
// request fields are dropped, methods are unimplemented and new enum values look unknown.
type releaseCheck struct {
	// ours is chalkctl's release.
	ours   string
	stderr io.Writer
	mu     sync.Mutex
	warned map[string]bool
}

// check warns once that the node called name runs chalkd of the release theirs, when that is
// known and not chalkctl's.
func (r *releaseCheck) check(name, theirs string) {
	if r == nil || !knownRelease(r.ours) || !knownRelease(theirs) || theirs == r.ours {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warned[theirs] {
		return
	}
	if r.warned == nil {
		r.warned = map[string]bool{}
	}
	r.warned[theirs] = true
	fmt.Fprintf(r.stderr, "chalkctl: warning: %s runs chalkd %s, this chalkctl is %s; use the chalkctl of the node's release\n", name, theirs, r.ours)
}

// knownRelease reports whether a program knows its release: a build that did not set it, such as
// go build's, does not.
func knownRelease(v string) bool { return v != "" && v != "unknown" }

// watch has check see the release of every Info answer of the node called name on conn.
func (r *releaseCheck) watch(conn *client.Conn, name string) *client.Conn {
	if r != nil {
		conn.NodeServiceClient = releaseClient{NodeServiceClient: conn.NodeServiceClient, name: name, check: r}
	}
	return conn
}

// releaseClient is a node's client whose Info answers go to check.
type releaseClient struct {
	nodev1connect.NodeServiceClient
	name  string
	check *releaseCheck
}

func (c releaseClient) Info(ctx context.Context, req *nodev1.InfoRequest) (*nodev1.InfoResponse, error) {
	resp, err := c.NodeServiceClient.Info(ctx, req)
	if err == nil {
		c.check.check(c.name, resp.GetChalkosVersion())
	}
	return resp, err
}
