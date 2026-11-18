package kubernetes

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	ggcrtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
)

func registryDeleteCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	cmd := h.Command(&cobra.Command{
		Use:     "delete REFERENCE",
		Short:   "Delete a tag or digest on every endpoint of the cluster registry",
		Long:    registryDeleteLong,
		Example: registryDeleteExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, args []string) error {
		ref, err := parseReference(args[0])
		if err != nil {
			return err
		}
		s, err := f.endpoints(h, ctx, "registry delete", pki.RoleAdmin)
		if err != nil {
			return err
		}
		if _, err := s.targetOf(s.Endpoints[0], ref); err != nil {
			return err
		}
		return s.delete(ctx, h.Stdout(), ref)
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	return cmd
}

func registryGCCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	cmd := h.Command(&cobra.Command{
		Use:     "gc",
		Short:   "Collect garbage on every endpoint of the cluster registry",
		Long:    registryGCLong,
		Example: registryGCExample,
		Args:    cli.NoArgs,
	}, func(h layer.Host, ctx context.Context, _ []string) error {
		s, err := f.endpoints(h, ctx, "registry gc", pki.RoleAdmin)
		if err != nil {
			return err
		}
		return s.collect(ctx, h.Stdout())
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	return cmd
}

func registryListCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	cmd := h.Command(&cobra.Command{
		Use:     "list",
		Short:   "List the images of every endpoint of the cluster registry and mark those that differ",
		Long:    registryListLong,
		Example: registryListExample,
		Args:    cli.NoArgs,
	}, func(h layer.Host, ctx context.Context, _ []string) error {
		s, err := f.endpoints(h, ctx, "registry list", pki.RoleReader)
		if err != nil {
			return err
		}
		return s.list(ctx, h.Table())
	})
	f.register(cmd.Flags(), pki.RoleReader)
	return cmd
}

// delete deletes ref, a tag or a digest, below every endpoint, and fails unless each deleted it
// or never held it.
func (s endpoints) delete(ctx context.Context, out io.Writer, ref name.Reference) error {
	var left []string
	for _, e := range s.Endpoints {
		target, err := s.targetOf(e, ref)
		if err == nil {
			err = remote.Delete(target, s.options(ctx, e)...)
			if isNotFound(err) {
				fmt.Fprintf(out, "%s: absent %s\n", e, target)
				continue
			}
		}
		if err != nil {
			fmt.Fprintf(out, "%s: failed: %v\n", e, err)
			left = append(left, e.String())
			continue
		}
		fmt.Fprintf(out, "%s: deleted %s\n", e, target)
	}
	if len(left) > 0 {
		return fmt.Errorf("the image may still be on %d of %d endpoints of the cluster registry: %s; delete it again once they are reachable", len(left), len(s.Endpoints), strings.Join(left, ", "))
	}
	return nil
}

// isNotFound reports whether err is a registry's answer that the repository, manifest or tag is
// unknown.
func isNotFound(err error) bool {
	te, ok := errors.AsType[*ggcrtransport.Error](err)
	return ok && te.StatusCode == http.StatusNotFound
}

// collect collects garbage on every endpoint in turn, through the endpoint's registry.GCPath, and
// reports the bytes each freed.
func (s endpoints) collect(ctx context.Context, out io.Writer) error {
	var failed []string
	for _, e := range s.Endpoints {
		freed, err := collectOn(ctx, &http.Client{Transport: s.transport(e)}, "https://"+s.Host(e)+registry.GCPath)
		if err != nil {
			fmt.Fprintf(out, "%s: failed: %v\n", e, err)
			failed = append(failed, e.String())
			continue
		}
		fmt.Fprintf(out, "%s: freed %s\n", e, size(freed))
	}
	if len(failed) > 0 {
		return fmt.Errorf("garbage collection failed on %d of %d endpoints of the cluster registry: %s", len(failed), len(s.Endpoints), strings.Join(failed, ", "))
	}
	return nil
}

// collectOn asks the registry at url to collect garbage and returns the bytes it freed.
func collectOn(ctx context.Context, client *http.Client, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, registryError(resp)
	}
	var res registry.GCResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}
	return res.FreedBytes, nil
}

// registryError is the message of a registry's error answer, as the distribution spec writes
// it, else its status.
func registryError(resp *http.Response) error {
	var body struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) == nil && len(body.Errors) > 0 && body.Errors[0].Message != "" {
		return errors.New(body.Errors[0].Message)
	}
	return errors.New(resp.Status)
}

// size is n bytes in the largest binary unit that keeps it at 1 or more.
func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, "KiB"
	for _, u := range []string{"MiB", "GiB", "TiB"} {
		if v < 1024 {
			break
		}
		v, unit = v/1024, u
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}

// image is a tag of a repository.
type image struct{ repo, tag string }

// list prints each tag of each repository with the endpoints that serve it, marks those missing
// on or differing between endpoints, and fails when any is or an endpoint cannot be listed.
func (s endpoints) list(ctx context.Context, tw *tabwriter.Writer) error {
	served := map[image]map[netip.Addr]v1.Hash{}
	var failed []string
	for _, e := range s.Endpoints {
		if err := s.listOn(ctx, e, served); err != nil {
			fmt.Fprintf(tw, "%s: failed: %v\n", e, err)
			failed = append(failed, e.String())
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(tw, "REPOSITORY\tTAG\tDIGEST\tENDPOINTS")
	uneven := 0
	for _, img := range slices.SortedFunc(maps.Keys(served), func(a, b image) int {
		return cmp.Or(cmp.Compare(a.repo, b.repo), cmp.Compare(a.tag, b.tag))
	}) {
		at := served[img]
		first := s.Endpoints[slices.IndexFunc(s.Endpoints, func(e netip.Addr) bool { _, ok := at[e]; return ok })]
		want := at[first]
		var serving, notes []string
		for _, e := range s.Endpoints {
			d, ok := at[e]
			switch {
			case !ok && !slices.Contains(failed, e.String()):
				notes = append(notes, "missing on "+e.String())
			case ok && d != want:
				notes = append(notes, fmt.Sprintf("differs on %s (%s)", e, short(d)))
			case ok:
				serving = append(serving, e.String())
			}
		}
		where := "all"
		if len(serving) < len(s.Endpoints) {
			where = strings.Join(append([]string{strings.Join(serving, ", ")}, notes...), "; ")
		}
		if len(notes) > 0 {
			uneven++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", img.repo, img.tag, short(want), where)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	switch {
	case len(failed) > 0:
		return fmt.Errorf("%d of %d endpoints of the cluster registry could not be listed: %s", len(failed), len(s.Endpoints), strings.Join(failed, ", "))
	case uneven > 0:
		return fmt.Errorf("the endpoints of the cluster registry differ in %d of %d images; push those again to make them alike", uneven, len(served))
	}
	return nil
}

// listOn adds what the endpoint e serves to served: every tag of every repository of its catalog,
// by the digest a HEAD of it gives.
func (s endpoints) listOn(ctx context.Context, e netip.Addr, served map[image]map[netip.Addr]v1.Hash) error {
	opts := s.options(ctx, e)
	reg, err := name.NewRegistry(s.Host(e))
	if err != nil {
		return err
	}
	repos, err := remote.Catalog(ctx, reg, opts...)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		r := reg.Repo(repo)
		tags, err := remote.List(r, opts...)
		if isNotFound(err) {
			// A repository whose every tag was deleted.
			continue
		}
		if err != nil {
			return err
		}
		for _, tag := range tags {
			desc, err := remote.Head(r.Tag(tag), opts...)
			if err != nil {
				return err
			}
			img := image{repo, tag}
			if served[img] == nil {
				served[img] = map[netip.Addr]v1.Hash{}
			}
			served[img][e] = desc.Digest
		}
	}
	return nil
}

// short is a digest as the table shows it: its algorithm and first 12 hex digits.
func short(d v1.Hash) string { return d.Algorithm + ":" + d.Hex[:12] }
