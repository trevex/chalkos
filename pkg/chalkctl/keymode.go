package chalkctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// warnedFiles are the files a command warned of, so it warns of each once.
type warnedFiles struct {
	mu   sync.Mutex
	seen map[string]bool
}

// first reports whether path was not warned of yet, and marks it; nil reports true.
func (w *warnedFiles) first(path string) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen[path] {
		return false
	}
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	w.seen[path] = true
	return true
}

// warnReadable warns, once per command, when the regular file at path, which holds what holds
// says and the user owns, grants its group or others any permission, as a copied or checked-out
// file does. It warns and never refuses: the file works all the same. A file of another owner,
// such as one in the Nix store, is left alone, as the user cannot chmod it.
func (a *app) warnReadable(path, holds string) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 == 0 || !ownFile(path, fi) || !a.warnedFiles.first(path) {
		return
	}
	fmt.Fprintf(a.stderr, "chalkctl: warning: %s holds %s and is readable by others (mode %04o); chmod 600 it\n", path, holds, fi.Mode().Perm())
}

// ownFile reports whether the user owns the file at path, which fi describes, outside the Nix
// store, whose files no one changes, root included.
func ownFile(path string, fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	return err == nil && !strings.HasPrefix(real, "/nix/store/")
}
