package command

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxLinks bounds the symbolic links a path of an image follows, as the kernel's limit does.
const maxLinks = 40

// Resolve is where the file at name, an absolute path of an image whose root is at root on this
// node, is, its symbolic links followed within the image: their absolute targets, such as /etc's
// links into /nix/store, name paths of the image, not of the running system. An error wraps
// fs.ErrNotExist when there is no such file.
func Resolve(root, name string) (string, error) {
	return resolve(name, func(p string) (string, error) { return filepath.Join(root, filepath.FromSlash(p)), nil })
}

// ResolveInStore is where the file at name, a path of an image's store, is on this node, where
// the store is at store, its symbolic links followed within the store, such as a package's bin/
// link into another package. A link that leads out of the store is an error that wraps
// fs.ErrNotExist: the image's store is all of the image there is.
func ResolveInStore(store, name string) (string, error) {
	return resolve(name, func(p string) (string, error) {
		switch p {
		case "/", "/nix", "/nix/store":
			return store, nil
		}
		if rest, ok := strings.CutPrefix(p, "/nix/store/"); ok {
			return filepath.Join(store, filepath.FromSlash(rest)), nil
		}
		return "", fmt.Errorf("%s is outside the image's store: %w", p, fs.ErrNotExist)
	})
}

// resolve follows name's symbolic links as paths of an image, which host maps to where they are
// on this node, and returns where the file is.
func resolve(name string, host func(string) (string, error)) (string, error) {
	if !path.IsAbs(name) {
		return "", fmt.Errorf("%s is not an absolute path of the image", name)
	}
	logical, rest := "/", strings.Split(path.Clean(name), "/")[1:]
	for links := 0; len(rest) > 0; {
		next := path.Join(logical, rest[0])
		rest = rest[1:]
		p, err := host(next)
		if err != nil {
			return "", fmt.Errorf("%s in the image: %w", name, err)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return "", fmt.Errorf("%s in the image: %w", name, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			logical = next
			continue
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("%s in the image: too many symbolic links", name)
		}
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(target) {
			target = path.Join(logical, target)
		}
		rest = append(strings.Split(path.Clean(target), "/")[1:], rest...)
		logical = "/"
	}
	return host(logical)
}
