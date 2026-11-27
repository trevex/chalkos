package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"connectrpc.com/connect/v2"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/imagecheck"
	"github.com/trevex/chalkos/pkg/storage"
)

// imageStore is the store of an image that does not run, as an upgrade wrote it and
// upgrade.Store reads it: by the image's own paths. Resolve is where the file at such a path is
// on this node, its links followed within the image, and Dir where its /nix/store is.
type imageStore interface {
	ReadFile(name string) ([]byte, error)
	Resolve(name string) (string, error)
	Dir() string
}

// imageCheck is config for an image that does not run, to check with that image's checker.
type imageCheck struct {
	// delivered is the config as delivered, whose Secret values the checker gets hashed; old is
	// the running slot's as delivered, none at an install from the installer. Both come without
	// defaults, so the checker compares them with the meaning its own defaults give them.
	delivered, old []config.Document
	// secret is the fallback secret, which only the facts that declare it get, run by chalkd.
	secret string
	// recorded, when set, stands for the storage STATE records, as a delivery to the running image
	// leaves it before the slot is written.
	recorded *storage.Section
	// image is the root hash of the image that checks, which its errors name; writing that of the
	// image whose slot is written, which a refusal of a storage change leaves out of those it
	// names.
	image, writing []byte
	// install reads every fact as null, as at an install in place: what an attempt before it left
	// on STATE is not the node's.
	install bool
}

// checkWithImage has the image in the store st, whose NixOS system is system, check c for the node
// called name on platform with its own chalkd, and returns the config slot it keeps, with the
// check's warnings. The running chalkd reads nothing of the image but its facts.json. The checker
// never sees Secret values: chalkd puts the delivered values back in place of the hashes once it
// made sure they match. The caller holds s.mu.
func (s *Server) checkWithImage(ctx context.Context, st imageStore, system, platform, name string, c imageCheck) (slot.Stored, []string, error) {
	res, err := s.runImageCheck(ctx, st, system, platform, name, c)
	if err != nil {
		return slot.Stored{}, nil, err
	}
	if r := res.GetRefusal(); r != nil {
		return slot.Stored{}, nil, s.imageRefusal(r, res.GetIdentity(), c.writing)
	}
	hashed, err := imagecheck.Decode(res.GetDocuments())
	if err != nil {
		return slot.Stored{}, nil, fmt.Errorf("the image's checker: %w", err)
	}
	docs, err := config.Restore(hashed, c.delivered)
	if err != nil {
		return slot.Stored{}, nil, fmt.Errorf("the image's checker gave back other documents than it got: %w", err)
	}
	version, err := config.Hash(c.delivered)
	if err != nil {
		return slot.Stored{}, nil, layer.Errorf(connect.CodeInvalidArgument, "hash the config: %w", err)
	}
	return slot.Stored{Delivered: c.delivered, Documents: docs, Version: version, Identity: []byte(res.GetIdentity())}, res.GetWarnings(), nil
}

// runImageCheck runs the checker of the image whose store is st on c and returns its result, a
// refusal among them.
func (s *Server) runImageCheck(ctx context.Context, st imageStore, system, platform, name string, c imageCheck) (*checkv1.Result, error) {
	checker, err := st.Resolve(system + imagecheck.CheckerPath)
	if err != nil {
		return nil, fmt.Errorf("find the image's checker: %w", err)
	}
	f, err := s.stagedFacts(ctx, st, system, c)
	if err != nil {
		return nil, err
	}
	in := &checkv1.Input{Node: name, Platform: platform}
	if in.Delivered, err = imagecheck.Encode(config.Redact(c.delivered)); err != nil {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "encode the config: %w", err)
	}
	if in.Old, err = imagecheck.Encode(config.Redact(c.old)); err != nil {
		return nil, fmt.Errorf("encode the running config: %w", err)
	}
	run := s.CheckImage
	if run == nil {
		run = imagecheck.DefaultSandbox(filepath.Join(s.Paths.RunDir, "check"))
	}
	res, err := run.Run(ctx, imagecheck.Image{Store: st.Dir(), System: system, Checker: checker}, in, f)
	if err != nil {
		return nil, fmt.Errorf("run the image's checker: %w", err)
	}
	if e := res.GetError(); e != "" {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the image cannot check config: %s", e)
	}
	return res, nil
}

// imageRefusal is the error of a config the image refused, as a delivery to the running image
// refuses one: the checker's *check.Refusal, as refusal gives it. A refusal by the rules names the
// volumes of the identity the image gave that other images' slots name, as elsewhere does, on a
// node: the installer has neither volumes nor slots.
func (s *Server) imageRefusal(r *checkv1.Refusal, identity string, writing []byte) error {
	stage := check.Stage(r.GetStage())
	err := refusal(&check.Refusal{Stage: stage, Message: r.GetMessage(), Reason: r.GetReason()})
	if stage != check.StageRules || s.Installer {
		return err
	}
	if d, perr := s.parseIdentity(identity); perr == nil {
		return s.elsewhere(err, d.section, writing)
	}
	return err
}

// stagedFacts are the facts the image whose store is st declares, which chalkd loads for its
// checker: files of this node, and commands of the image's store, run in the facts sandbox with
// that store as its /nix/store. A missing facts.json declares none.
func (s *Server) stagedFacts(ctx context.Context, st imageStore, system string, c imageCheck) (imagecheck.Facts, error) {
	defs := map[string]facts.Definition{}
	path := filepath.Join(system, contract.DefaultDir, contract.FactsFile)
	data, err := st.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read the image's facts: %w", err)
	default:
		if defs, err = facts.Parse(path, data); err != nil {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "the image's facts: %w", err)
		}
	}
	run := s.Facts
	if run == nil {
		run = facts.Sandbox{Store: st.Dir()}
	}
	l := facts.NewLoader(ctx, defs, run)
	l.Inputs = map[string]any{facts.FallbackSecret: c.secret}
	l.Install = c.install
	sf := servedFacts{l: l, overrides: map[string]any{}}
	if c.recorded != nil {
		sf.overrides["recorded"] = jsonValue(*c.recorded)
	}
	return sf, nil
}

// servedFacts loads the facts a checker asks for, each for the input it gives, with the overrides
// in place of what the node holds. imagecheck asks it for one fact at a time, as the Loader needs.
type servedFacts struct {
	l         *facts.Loader
	overrides map[string]any
}

func (f servedFacts) Fact(name string, input map[string]any) (any, error) {
	l := f.l.For(input)
	for k, v := range f.overrides {
		l.Override(k, v)
	}
	return l.Fact(name)
}
