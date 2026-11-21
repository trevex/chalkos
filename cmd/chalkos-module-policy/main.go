// Command chalkos-module-policy loads the image's signed IPE policy in the initrd, so the kernel
// loads modules from the image's verity store and initrd alone. It records what happened in
// /run/chalkos/module-policy.json for chalkd and never fails the boot: a node without Secure
// Boot, or whose db lacks the key, boots without the policy and reports it.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/trevex/chalkos/pkg/modulepolicy"
)

const usage = `usage: chalkos-module-policy load

modes:
  load  load and activate the image's module policy, and record the outcome (initrd)`

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkos-module-policy: ")
	if len(os.Args) != 2 || os.Args[1] != "load" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	r := modulepolicy.Load(modulepolicy.InitrdPaths())
	switch r.State {
	case modulepolicy.StateLoaded:
		log.Printf("loaded and activated the module policy %s %s", modulepolicy.Name, r.Version)
	default:
		log.Printf("the module policy is not in force: %s", r.Error)
	}
}
