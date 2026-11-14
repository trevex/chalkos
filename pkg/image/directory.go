package image

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindRaw returns the path of the one raw image in an image directory.
func FindRaw(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var raws []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".raw" {
			raws = append(raws, e.Name())
		}
	}
	if len(raws) != 1 {
		return "", fmt.Errorf("want exactly one .raw image in %s, found %v", dir, raws)
	}
	return filepath.Join(dir, raws[0]), nil
}
