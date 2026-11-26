package kubernetes

import (
	"bytes"
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

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	ggcrtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/statusview"
)

func registryDeleteCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	var withTags bool
	cmd := h.Command(&cobra.Command{
		Use:         "delete REFERENCE",
		Short:       "Delete a tag or digest on every endpoint of the cluster registry",
		Long:        registryDeleteLong,
		Example:     registryDeleteExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.Admin, Confirms: registryDeleteConfirms}.Annotations(),
	}, func(h layer.Host, ctx context.Context, args []string) error {
		ref, err := parseImageRef(args[0])
		switch {
		case err != nil:
			return err
		case ref.tag != "" && ref.digest != "":
			return cli.Usagef("%s names a tag and a digest; delete the tag %s:%s or the digest %s@%s", ref, ref.name(), ref.tag, ref.name(), ref.digest)
		case withTags && ref.digest == "":
			return cli.Usagef("--with-tags deletes the tags of a digest; %s names a tag", ref)
		}
		s, err := f.endpoints(h, ctx, "registry delete", pki.RoleAdmin)
		if err != nil {
			return err
		}
		if _, err := s.repoAt(s.Endpoints[0], ref); err != nil {
			return err
		}
		return s.delete(ctx, h.Stdout(), h.Stderr(), ref, withTags, func(what string) error { return h.Confirm(ctx, what, "") })
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	cmd.Flags().BoolVar(&withTags, "with-tags", false, "delete a digest together with the tags that name it, which the command refuses otherwise")
	return cmd
}

func registryGCCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	cmd := h.Command(&cobra.Command{
		Use:         "gc",
		Short:       "Collect garbage on every endpoint of the cluster registry",
		Long:        registryGCLong,
		Example:     registryGCExample,
		Args:        cli.NoArgs,
		Annotations: layer.Meta{Credentials: layer.Admin}.Annotations(),
	}, func(h layer.Host, ctx context.Context, _ []string) error {
		s, err := f.endpoints(h, ctx, "registry gc", pki.RoleAdmin)
		if err != nil {
			return err
		}
		return s.collect(ctx, h.Stdout(), h.Stderr())
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	return cmd
}

func registryListCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	var output layer.Output
	cmd := h.Command(&cobra.Command{
		Use:         "list",
		Short:       "List the images of every endpoint of the cluster registry and mark those that differ",
		Long:        registryListLong,
		Example:     registryListExample,
		Args:        cli.NoArgs,
		Annotations: layer.Meta{Credentials: layer.Reader}.Annotations(),
	}, func(h layer.Host, ctx context.Context, _ []string) error {
		s, err := f.endpoints(h, ctx, "registry list", pki.RoleReader)
		if err != nil {
			return err
		}
		return s.list(ctx, h.Stdout(), h.Stderr(), output)
	})
	f.register(cmd.Flags(), pki.RoleReader)
	output.Register(cmd.Flags())
	return cmd
}

// tagsOn returns the tags of repo on the endpoint e, each with the descriptor of the manifest it
// names; none when the endpoint lacks the repository.
func (s endpoints) tagsOn(ctx context.Context, e netip.Addr, repo name.Repository) (map[string]v1.Descriptor, error) {
	opts := s.options(ctx, e)
	tags, err := remote.List(repo, opts...)
	if isUnknown(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]v1.Descriptor{}
	for _, tag := range tags {
		desc, err := remote.Head(repo.Tag(tag), opts...)
		if headNotFound(err) {
			// Deleted since the list.
			continue
		}
		if err != nil {
			return nil, err
		}
		out[tag] = *desc
	}
	return out, nil
}

// childrenOf returns the manifests the index d of repo on the endpoint e names.
func (s endpoints) childrenOf(ctx context.Context, e netip.Addr, repo name.Repository, d v1.Hash) ([]v1.Descriptor, error) {
	desc, err := remote.Get(repo.Digest(d.String()), s.options(ctx, e)...)
	if err != nil {
		return nil, err
	}
	m, err := v1.ParseIndexManifest(bytes.NewReader(desc.Manifest))
	if err != nil {
		return nil, err
	}
	return m.Manifests, nil
}

// readRepo reads the tags of repo on the endpoint e and, for a digest ref, the tagged indexes
// that name the digest.
func (s endpoints) readRepo(ctx context.Context, e netip.Addr, repo name.Repository, ref imageRef) (map[string]v1.Descriptor, []string, error) {
	tags, err := s.tagsOn(ctx, e, repo)
	if err != nil || ref.digest == "" {
		return tags, nil, err
	}
	parents, err := s.parentsOf(ctx, e, repo, tags, ref.digest)
	return tags, parents, err
}

// delete deletes ref, a tag or a digest, below every endpoint, and fails unless each deleted it
// or never held it. Before it deletes anything, it refuses a digest that a tagged index names, as
// deleting it would break the index, and a digest that tags name, unless withTags and confirm
// accepts deleting the tags; as the prompt may wait for long, it then reads the tags again and
// refuses when a tagged index names the digest or the tags naming it changed. It reports each
// endpoint, a failed one to errOut.
func (s endpoints) delete(ctx context.Context, out, errOut io.Writer, ref imageRef, withTags bool, confirm func(what string) error) error {
	var failed []string
	fail := func(e netip.Addr, err error) {
		fmt.Fprintf(errOut, "%s: failed: %v\n", e, err)
		failed = append(failed, e.String())
	}
	brokenIndex := func(parents []string) error {
		return fmt.Errorf("%s is an image of the index %s; deleting it would break the index, so delete the index first", ref, strs.And(parents))
	}
	// The tags of the repository on each endpoint that could be read.
	tagged := map[netip.Addr]map[string]v1.Descriptor{}
	var naming, parents, namingOn []string
	for _, e := range s.Endpoints {
		repo, err := s.repoAt(e, ref)
		if err != nil {
			return err
		}
		tags, on, err := s.readRepo(ctx, e, repo, ref)
		parents = append(parents, on...)
		if err != nil {
			fail(e, err)
			continue
		}
		tagged[e] = tags
		if ref.digest != "" {
			if names := tagsNaming(tags, ref.digest); len(names) > 0 {
				naming = append(naming, names...)
				namingOn = append(namingOn, e.String())
			}
		}
	}
	if len(parents) > 0 {
		return brokenIndex(parents)
	}
	slices.Sort(naming)
	naming = slices.Compact(naming)
	if len(naming) > 0 && !withTags {
		return fmt.Errorf("the tags %s name %s on %s; pass --with-tags to delete them with it, or delete the tags alone", strs.And(naming), ref, strs.And(namingOn))
	}
	if len(naming) > 0 {
		if err := confirm(fmt.Sprintf("registry delete deletes %s with the tags %s that name it on %s", ref, strs.And(naming), strs.And(namingOn))); err != nil {
			return err
		}
		for _, e := range s.Endpoints {
			confirmed, ok := tagged[e]
			if !ok {
				continue
			}
			repo, _ := s.repoAt(e, ref)
			tags, on, err := s.readRepo(ctx, e, repo, ref)
			switch {
			case err != nil:
				return fmt.Errorf("%s: %w", e, err)
			case len(on) > 0:
				return brokenIndex(on)
			case !slices.Equal(tagsNaming(tags, ref.digest), tagsNaming(confirmed, ref.digest)):
				return fmt.Errorf("the tags that name %s on %s changed while the prompt waited; delete again", ref, e)
			}
			tagged[e] = tags
		}
	}
	// Digests that no tag names any more once their tag went.
	untagged := map[string]bool{}
	for _, e := range s.Endpoints {
		tags, ok := tagged[e]
		if !ok {
			continue
		}
		target, _ := s.targetOf(e, ref)
		var named v1.Descriptor
		if ref.tag != "" {
			if named, ok = tags[ref.tag]; !ok {
				fmt.Fprintf(out, "%s: absent %s\n", e, target)
				continue
			}
		}
		err := remote.Delete(target, s.options(ctx, e)...)
		switch {
		case isUnknown(err):
			fmt.Fprintf(out, "%s: absent %s\n", e, target)
		case err != nil:
			fail(e, err)
		case ref.tag == "":
			line := fmt.Sprintf("%s: deleted %s", e, target)
			if names := tagsNaming(tags, ref.digest); len(names) > 0 {
				line += fmt.Sprintf(" and its tags %s", strs.And(names))
			}
			fmt.Fprintln(out, line)
		default:
			d := named.Digest.String()
			line := fmt.Sprintf("%s: deleted %s, which named %s", e, target, d)
			if others := slices.DeleteFunc(tagsNaming(tags, d), func(t string) bool { return t == ref.tag }); len(others) > 0 {
				line += fmt.Sprintf("; the tags %s still name it", strs.And(others))
			} else {
				line += "; no tag names it now"
				untagged[d] = true
			}
			fmt.Fprintln(out, line)
		}
	}
	for _, d := range slices.Sorted(maps.Keys(untagged)) {
		fmt.Fprintf(out, "the manifest %s stays until chalkctl registry delete %s@%s; chalkctl registry gc then frees its layers\n", d, ref.name(), d)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the delete failed on %d of %d endpoints of the cluster registry: %s; delete again once they are reachable", len(failed), len(s.Endpoints), strings.Join(failed, ", "))
	}
	return nil
}

// tagsNaming are the tags that name the digest d, sorted.
func tagsNaming(tags map[string]v1.Descriptor, d string) []string {
	var out []string
	for t, desc := range tags {
		if desc.Digest.String() == d {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// parentsOf names the tagged indexes of repo on the endpoint e that name the digest d, as
// <repository>:<tag> on <endpoint>.
func (s endpoints) parentsOf(ctx context.Context, e netip.Addr, repo name.Repository, tags map[string]v1.Descriptor, d string) ([]string, error) {
	var out []string
	for _, t := range slices.Sorted(maps.Keys(tags)) {
		desc := tags[t]
		if !desc.MediaType.IsIndex() || desc.Digest.String() == d {
			continue
		}
		children, err := s.childrenOf(ctx, e, repo, desc.Digest)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(children, func(c v1.Descriptor) bool { return c.Digest.String() == d }) {
			out = append(out, fmt.Sprintf("%s:%s on %s", repo.RepositoryStr(), t, e))
		}
	}
	return out, nil
}

// isUnknown reports whether err is a registry's answer that the repository, manifest or tag is
// unknown.
func isUnknown(err error) bool {
	te, ok := errors.AsType[*ggcrtransport.Error](err)
	if !ok {
		return false
	}
	return slices.ContainsFunc(te.Errors, func(d ggcrtransport.Diagnostic) bool {
		switch d.Code {
		case ggcrtransport.ManifestUnknownErrorCode, ggcrtransport.NameUnknownErrorCode, "TAG_UNKNOWN":
			return true
		}
		return false
	})
}

// headNotFound reports whether err is a registry's answer to a HEAD, which has no body, that it
// lacks the manifest.
func headNotFound(err error) bool {
	te, ok := errors.AsType[*ggcrtransport.Error](err)
	return ok && te.StatusCode == http.StatusNotFound
}

// collect collects garbage on every endpoint in turn, through the endpoint's registry.GCPath, and
// reports the bytes each freed, a failed endpoint to errOut.
func (s endpoints) collect(ctx context.Context, out, errOut io.Writer) error {
	var failed []string
	for _, e := range s.Endpoints {
		freed, err := collectOn(ctx, &http.Client{Transport: s.transport(e)}, "https://"+s.Host(e)+registry.GCPath)
		if err != nil {
			fmt.Fprintf(errOut, "%s: failed: %v\n", e, err)
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

// held is what an endpoint serves for a tag: the manifest's digest and, for an index, how many
// of the manifests it names the endpoint lacks.
type held struct {
	digest  v1.Hash
	missing int
}

// registryListing is what registry list finds on the endpoints of the cluster registry, as
// registry list -o json prints it.
type registryListing struct {
	// Endpoints are the endpoints asked, in their order.
	Endpoints []string `json:"endpoints"`
	// Failed are the endpoints that could not be listed.
	Failed []string `json:"failed,omitempty"`
	// Images are the tags of every repository, by repository and tag.
	Images []registryImage `json:"images"`
}

// registryImage is a tag of a repository of the cluster registry, as the endpoints serve it.
type registryImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	// Digest is the digest of the manifest most endpoints serve for the tag, the first in the
	// endpoints' order on a tie.
	Digest string `json:"digest"`
	// Serving are the endpoints that serve Digest whole.
	Serving []string `json:"serving"`
	// Missing are the endpoints, of those listed, that lack the tag.
	Missing []string `json:"missing,omitempty"`
	// Differs are the digests of the endpoints that serve another manifest, by the endpoint.
	Differs map[string]string `json:"differs,omitempty"`
	// Broken are how many images of the index each endpoint that lacks some of them lacks, by the
	// endpoint.
	Broken map[string]int `json:"broken,omitempty"`
}

// marked reports whether some endpoint lacks the image, serves another one or lacks part of it.
func (img registryImage) marked() bool {
	return len(img.Missing) > 0 || len(img.Differs) > 0 || len(img.Broken) > 0
}

// list prints each tag of each repository with the endpoints that serve it, and marks those
// missing on, differing between, or broken on some endpoint: an index whose images an endpoint
// lacks. A tag's digest is the one most endpoints serve, the first in endpoints order on a tie.
// With -o json it prints the listing. It fails when any is marked or an endpoint cannot be
// listed, which it reports to errOut.
func (s endpoints) list(ctx context.Context, out, errOut io.Writer, output layer.Output) error {
	l := s.listing(ctx, errOut)
	if output.JSON() {
		if err := layer.WriteJSON(out, l); err != nil {
			return err
		}
	} else if err := s.writeListing(out, l); err != nil {
		return err
	}
	marked := 0
	for _, img := range l.Images {
		if img.marked() {
			marked++
		}
	}
	switch {
	case len(l.Failed) > 0:
		return fmt.Errorf("%d of %d endpoints of the cluster registry could not be listed: %s", len(l.Failed), len(s.Endpoints), strings.Join(l.Failed, ", "))
	case marked > 0:
		return fmt.Errorf("the endpoints of the cluster registry differ in %d of %d images; push those again to every endpoint to make them alike", marked, len(l.Images))
	}
	return nil
}

// listing lists what every endpoint serves, and reports to errOut each endpoint that cannot be
// listed.
func (s endpoints) listing(ctx context.Context, errOut io.Writer) registryListing {
	served := map[image]map[netip.Addr]held{}
	l := registryListing{Images: []registryImage{}}
	for _, e := range s.Endpoints {
		l.Endpoints = append(l.Endpoints, e.String())
		if err := s.listOn(ctx, e, served); err != nil {
			fmt.Fprintf(errOut, "%s: failed: %v\n", e, err)
			l.Failed = append(l.Failed, e.String())
		}
	}
	for _, img := range slices.SortedFunc(maps.Keys(served), func(a, b image) int {
		return cmp.Or(cmp.Compare(a.repo, b.repo), cmp.Compare(a.tag, b.tag))
	}) {
		at := served[img]
		want := s.majority(at)
		r := registryImage{Repository: img.repo, Tag: img.tag, Digest: want.String(), Serving: []string{}}
		for _, e := range s.Endpoints {
			h, ok := at[e]
			switch {
			case !ok && !slices.Contains(l.Failed, e.String()):
				r.Missing = append(r.Missing, e.String())
			case !ok:
			case h.digest != want:
				if r.Differs == nil {
					r.Differs = map[string]string{}
				}
				r.Differs[e.String()] = h.digest.String()
			case h.missing > 0:
				if r.Broken == nil {
					r.Broken = map[string]int{}
				}
				r.Broken[e.String()] = h.missing
			default:
				r.Serving = append(r.Serving, e.String())
			}
		}
		l.Images = append(l.Images, r)
	}
	return l
}

// writeListing writes the table of the listing: each image with the endpoints that serve it, or
// all, and the notes of those that do not.
func (s endpoints) writeListing(out io.Writer, l registryListing) error {
	tw := statusview.Table(out)
	fmt.Fprintln(tw, "REPOSITORY\tTAG\tDIGEST\tENDPOINTS")
	for _, img := range l.Images {
		var notes []string
		for _, e := range s.Endpoints {
			switch e := e.String(); {
			case slices.Contains(img.Missing, e):
				notes = append(notes, "missing on "+e)
			case img.Differs[e] != "":
				notes = append(notes, fmt.Sprintf("differs on %s (%s)", e, shortDigest(img.Differs[e])))
			case img.Broken[e] > 0:
				notes = append(notes, fmt.Sprintf("broken on %s (lacks %s of the index)", e, strs.Count(img.Broken[e], "image")))
			}
		}
		where := "all"
		if len(img.Serving) < len(s.Endpoints) {
			where = strings.Join(append([]string{strings.Join(img.Serving, ", ")}, notes...), "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", img.Repository, img.Tag, shortDigest(img.Digest), where)
	}
	return tw.Flush()
}

// majority is the digest most endpoints of at serve, the first in endpoints order on a tie.
func (s endpoints) majority(at map[netip.Addr]held) v1.Hash {
	counts := map[v1.Hash]int{}
	for _, e := range s.Endpoints {
		if h, ok := at[e]; ok {
			counts[h.digest]++
		}
	}
	var best v1.Hash
	for _, e := range s.Endpoints {
		if h, ok := at[e]; ok && counts[h.digest] > counts[best] {
			best = h.digest
		}
	}
	return best
}

// listOn adds what the endpoint e serves to served: every tag of every repository of its
// catalog, by the digest a HEAD of it gives, and for an index how many of its images e lacks.
func (s endpoints) listOn(ctx context.Context, e netip.Addr, served map[image]map[netip.Addr]held) error {
	opts := s.options(ctx, e)
	reg, err := name.NewRegistry(s.Host(e))
	if err != nil {
		return err
	}
	repos, err := remote.Catalog(ctx, reg, opts...)
	if err != nil {
		return err
	}
	for _, path := range repos {
		repo := reg.Repo(path)
		tags, err := s.tagsOn(ctx, e, repo)
		if err != nil {
			return err
		}
		for tag, desc := range tags {
			h := held{digest: desc.Digest}
			if desc.MediaType.IsIndex() {
				if h.missing, err = s.missingOf(ctx, e, repo, desc.Digest); err != nil {
					return err
				}
			}
			img := image{path, tag}
			if served[img] == nil {
				served[img] = map[netip.Addr]held{}
			}
			served[img][e] = h
		}
	}
	return nil
}

// missingOf counts the manifests the index d of repo names that the endpoint e lacks.
func (s endpoints) missingOf(ctx context.Context, e netip.Addr, repo name.Repository, d v1.Hash) (int, error) {
	children, err := s.childrenOf(ctx, e, repo, d)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range children {
		_, err := remote.Head(repo.Digest(c.Digest.String()), s.options(ctx, e)...)
		switch {
		case headNotFound(err):
			n++
		case err != nil:
			return 0, err
		}
	}
	return n, nil
}

// short is a digest as the table shows it: its algorithm and first 12 hex digits.
func short(d v1.Hash) string { return d.Algorithm + ":" + d.Hex[:12] }

// shortDigest is short of a digest in its string form, algorithm:hex.
func shortDigest(d string) string {
	alg, hex, _ := strings.Cut(d, ":")
	return alg + ":" + hex[:min(12, len(hex))]
}
