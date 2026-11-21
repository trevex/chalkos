package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// The renderers and validators of jq programs, chalkosLib.render.jq and chalkosLib.validate.jq:
// jq reads the input on standard input, and what it prints is the file or the report.

// renderJQ runs the jq program at program with the jq binary jq on the input on stdin, and writes
// what it prints, a string raw, to out/file.
func renderJQ(jq, program, file string, stdin io.Reader, out string) error {
	if !filepath.IsLocal(file) {
		return fmt.Errorf("%q is no file name below $OUT", file)
	}
	path := filepath.Join(out, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	err = runJQ(jq, "--raw-output", program, stdin, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// runJQ runs the jq program at program on stdin, with the flag that sets how jq prints, and
// prints what it prints on stdout. What jq writes to standard error goes to the journal.
func runJQ(jq, flag, program string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.Command(jq, flag, "--from-file", program)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("jq program %s: %w", program, err)
	}
	return nil
}
