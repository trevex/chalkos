// Package imageref reads container image references as config names them: a repository with its
// registry, a tag, a digest, or a tag followed by a digest. A tag of the form vX.Y.Z, with an
// optional suffix, carries the version of what the image runs.
package imageref

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Reference is what Parse reads of an image reference besides its repository.
type Reference struct {
	// Tag is the tag, empty for a reference by digest alone.
	Tag string
	// Digest is the digest, such as sha256:…; empty for a reference by tag alone.
	Digest string
}

// Parse reads a reference that names its registry and a tag, a digest, or both, as
// repository:tag@sha256:<digest>. An error names s and wraps what is wrong with it.
func Parse(s string) (Reference, error) {
	// go-containerregistry takes https: for a registry.
	if strings.Contains(s, "://") {
		return Reference{}, fmt.Errorf("%q is no image reference: %w", s, errors.New("a reference names no URL scheme"))
	}
	base, digest, hasDigest := strings.Cut(s, "@")
	var r Reference
	if hasDigest {
		if _, err := name.NewDigest(s, name.StrictValidation); err != nil {
			return Reference{}, fmt.Errorf("%q is no image reference: %w", s, err)
		}
		r.Digest = digest
		// NewDigest drops the tag; read it from what precedes the digest.
		if t, err := name.NewTag(base, name.StrictValidation); err == nil {
			r.Tag = t.TagStr()
		} else if _, err := name.NewRepository(base, name.StrictValidation); err != nil {
			return Reference{}, fmt.Errorf("%q is no image reference: %w", s, err)
		}
		return r, nil
	}
	t, err := name.NewTag(s, name.StrictValidation)
	if err != nil {
		return Reference{}, fmt.Errorf("%q is no image reference with a registry and a tag or digest: %w", s, err)
	}
	return Reference{Tag: t.TagStr()}, nil
}

// Version is a version as a tag names it, v<major>.<minor>.<patch> with an optional suffix
// such as -rc.1.
type Version struct {
	Major, Minor, Patch int
	// Suffix is what follows the patch, with its leading dash; empty without one.
	Suffix string
}

func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d%s", v.Major, v.Minor, v.Patch, v.Suffix)
}

// MinorString is the version's major and minor, such as 1.37.
func (v Version) MinorString() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

var versionTag = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

// ErrNoVersion is the error of a reference whose tag is no version.
var ErrNoVersion = errors.New("its tag is no version v<major>.<minor>.<patch>, optionally with a suffix such as -rc.1")

// ParseVersion reads a version with or without its leading v, such as 1.37.1 or v1.38.0-rc.1.
func ParseVersion(s string) (Version, error) {
	m := versionTag.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q is no version <major>.<minor>.<patch>", s)
	}
	var v Version
	for i, p := range []*int{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("%q is no version: %w", s, err)
		}
		*p = n
	}
	v.Suffix = m[4]
	return v, nil
}

// Version is the version the reference's tag names, which starts with v.
func (r Reference) Version() (Version, error) {
	if !strings.HasPrefix(r.Tag, "v") {
		return Version{}, ErrNoVersion
	}
	v, err := ParseVersion(r.Tag)
	if err != nil {
		return Version{}, ErrNoVersion
	}
	return v, nil
}

// Pinned reports whether the reference names a digest, which containerd pulls by.
func (r Reference) Pinned() bool { return r.Digest != "" }

// Release is the version without its leading v, such as 1.37.1, as image.json names the kubelet's.
func (v Version) Release() string { return strings.TrimPrefix(v.String(), "v") }

// Minors is how many minors v is newer than o, of the same major; negative when it is older.
func (v Version) Minors(o Version) int { return v.Minor - o.Minor }
