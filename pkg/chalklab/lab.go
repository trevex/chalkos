package chalklab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/lab"
)

// noLabError is what labDir fails with when there is no lab to find.
type noLabError string

func (e noLabError) Error() string { return string(e) + "; chalklab create starts one" }

// labDir finds the lab of the cluster named, or the only lab there is.
func labDir(cluster string) (string, *lab.Lab, error) {
	if cluster == "" {
		root, err := lab.StateRoot()
		if err != nil {
			return "", nil, err
		}
		entries, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		switch len(names) {
		case 0:
			return "", nil, noLabError("there is no lab in " + root)
		case 1:
			cluster = names[0]
		default:
			return "", nil, fmt.Errorf("there are labs of %v in %s; choose one with --cluster", names, root)
		}
	}
	dir, err := lab.StateDir(cluster)
	if err != nil {
		return "", nil, err
	}
	l, err := lab.ReadLab(dir)
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(dir); statErr == nil {
			// A create that failed before it recorded the lab; destroy removes what it left.
			return dir, nil, nil
		}
		return "", nil, noLabError("there is no lab of " + cluster + " in " + dir)
	}
	return dir, l, err
}

func (a *app) statusCommand() *cobra.Command {
	var cluster string
	cmd := a.command(&cobra.Command{
		Use:     "status",
		Short:   "Show the lab's VMs, ports and files",
		Long:    statusLong,
		Example: statusExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.status(cluster, pos) })
	registerCluster(cmd, &cluster, "cluster whose lab to show (default the only lab)")
	return cmd
}

func (a *app) status(cluster string, pos []string) error {
	if len(pos) != 0 {
		return errors.New("usage: chalklab status [--cluster NAME]")
	}
	dir, l, err := labDir(cluster)
	if err != nil {
		return err
	}
	if l == nil {
		return fmt.Errorf("%s holds no lab; chalklab destroy removes it", dir)
	}
	supervisor := "no supervisor runs it"
	pid, running, err := lab.Supervisor(dir)
	if err != nil {
		return err
	}
	if running {
		supervisor = "its supervisor runs as PID " + strconv.Itoa(pid)
	}
	fmt.Fprintf(a.stdout, "lab of %s in %s; %s\n", l.Cluster, dir, supervisor)
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	var stopped []string
	fmt.Fprintln(w, "NODE\tROLE\tVM\tCHALKD\tAPI SERVER\tCONSOLE")
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		state := "running"
		if !lab.Running(c) {
			state = "stopped"
			stopped = append(stopped, n.Name)
		}
		api := "-"
		if n.APIPort != 0 {
			api = "127.0.0.1:" + strconv.Itoa(n.APIPort)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t127.0.0.1:%d\t%s\t%s\n", n.Name, n.Role, state, n.ChalkdPort, api, c.ConsolePath())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(stopped) > 0 {
		verb := "does"
		if len(stopped) > 1 {
			verb = "do"
		}
		fmt.Fprintf(a.stdout, "%s %s not run; chalklab start starts the lab again\n", joinNames(stopped), verb)
	}
	for _, f := range []struct{ what, file string }{
		{"kubeconfig", kubeconfigFile}, {"client file", clientFile},
		{"Secure Boot db key", filepath.Join("keys", "db.key")}, {"Secure Boot db certificate", filepath.Join("keys", "db.crt")},
	} {
		if _, err := os.Stat(filepath.Join(dir, f.file)); err == nil {
			fmt.Fprintf(a.stdout, "%s: %s\n", f.what, filepath.Join(dir, f.file))
		}
	}
	return nil
}

// console prints a node's console log and what the node writes to it, until interrupted.
func (a *app) consoleCommand() *cobra.Command {
	var cluster string
	var follow bool
	cmd := a.command(&cobra.Command{
		Use:     "console <node>",
		Short:   "Follow a node's serial console",
		Long:    consoleLong,
		Example: consoleExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.console(ctx, cluster, follow, pos) })
	registerCluster(cmd, &cluster, "cluster of the node's lab (default the only lab)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "keep printing what the node writes")
	return cmd
}

func (a *app) console(ctx context.Context, cluster string, follow bool, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalklab console <node> [--cluster NAME] [-f=false]")
	}
	dir, l, err := labDir(cluster)
	if err != nil {
		return err
	}
	if l == nil {
		return fmt.Errorf("%s holds no lab", dir)
	}
	n, err := l.Node(pos[0])
	if err != nil {
		return err
	}
	f, err := os.Open(l.VMConfig(dir, n).ConsolePath())
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if _, err := io.Copy(a.stdout, f); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// sign signs an image with the lab's db key, in place or as a copy, so the lab's firmware boots
// it, as an upgrade's image.
func (a *app) signCommand() *cobra.Command {
	var cluster, out string
	cmd := a.command(&cobra.Command{
		Use:     "sign <image>",
		Short:   "Sign an image with the lab's Secure Boot keys, for upgrades",
		Long:    signLong,
		Example: signExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.sign(ctx, cluster, out, pos) })
	registerCluster(cmd, &cluster, "cluster whose lab's keys to sign with (default the only lab)")
	cmd.Flags().StringVar(&out, "out", "", "directory to copy the image to (made when missing) and sign there; needed for an image in the Nix store, which cannot be signed in place")
	return cmd
}

func (a *app) sign(ctx context.Context, cluster, out string, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalklab sign <image> [--out DIR] [--cluster NAME]")
	}
	dir, l, err := labDir(cluster)
	if err != nil {
		return err
	}
	if l == nil {
		return fmt.Errorf("%s holds no lab", dir)
	}
	raw, partitions, err := imageFiles(pos[0])
	if err != nil {
		return err
	}
	if out != "" {
		if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
		copied := filepath.Join(out, filepath.Base(raw))
		if err := lab.CopySparse(ctx, raw, copied); err != nil {
			return err
		}
		data, err := os.ReadFile(partitions)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, filepath.Base(partitions)), data, 0o644); err != nil {
			return err
		}
		if err := copyDir(filepath.Join(filepath.Dir(raw), "repart.d"), filepath.Join(out, "repart.d")); err != nil {
			return err
		}
		raw, partitions = copied, filepath.Join(out, filepath.Base(partitions))
	} else if f, err := os.OpenFile(raw, os.O_WRONLY, 0); err != nil {
		return fmt.Errorf("%w; sign a copy with --out DIR", err)
	} else {
		f.Close()
	}
	if err := signImage(ctx, raw, partitions, filepath.Join(dir, "keys")); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "signed %s with the db key of the lab of %s\n", raw, l.Cluster)
	return nil
}

// copyDir copies the files of a directory, as an image's repart.d, when it exists.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// destroy stops the lab and removes its state: disks, firmware variables, TPM state, keys, logs.
func (a *app) destroyCommand() *cobra.Command {
	var cluster string
	cmd := a.command(&cobra.Command{
		Use:     "destroy",
		Short:   "Stop the lab and remove its state",
		Long:    destroyLong,
		Example: destroyExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.destroy(ctx, cluster, pos) })
	registerCluster(cmd, &cluster, "cluster whose lab to remove (default the only lab)")
	return cmd
}

func (a *app) destroy(ctx context.Context, cluster string, pos []string) error {
	if len(pos) != 0 {
		return errors.New("usage: chalklab destroy [--cluster NAME]")
	}
	dir, _, err := labDir(cluster)
	if e, ok := errors.AsType[noLabError](err); ok {
		fmt.Fprintf(a.stdout, "%s; nothing to destroy\n", string(e))
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := lab.StopLab(ctx, dir); err != nil {
		return fmt.Errorf("stop the lab, so its state is kept: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "removed the lab in %s\n", dir)
	return nil
}

// joinNames lists names as in "cp1, cp2 and w1".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// start starts the VMs of a lab again from its state, with their disks, firmware variables, TPM
// state and ports: through its supervisor the VMs that exited, or a lab that no supervisor runs,
// as after a host's reboot or a supervisor that was killed.
func (a *app) startCommand() *cobra.Command {
	var cluster string
	cmd := a.command(&cobra.Command{
		Use:     "start",
		Short:   "Start a stopped lab, or the VMs of a running lab that stopped",
		Long:    startLong,
		Example: startExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.start(ctx, cluster, pos) })
	registerCluster(cmd, &cluster, "cluster whose lab to start (default the only lab)")
	return cmd
}

func (a *app) start(ctx context.Context, cluster string, pos []string) error {
	if len(pos) != 0 {
		return errors.New("usage: chalklab start [--cluster NAME]")
	}
	dir, l, err := labDir(cluster)
	if err != nil {
		return err
	}
	if l == nil {
		return fmt.Errorf("%s holds no lab; chalklab destroy removes it", dir)
	}
	if err := checkKVM(); err != nil {
		return err
	}
	if _, running, err := lab.Supervisor(dir); err != nil {
		return err
	} else if running {
		var stopped []string
		for _, n := range l.Nodes {
			if !lab.Running(l.VMConfig(dir, n)) {
				stopped = append(stopped, n.Name)
			}
		}
		if len(stopped) == 0 {
			fmt.Fprintf(a.stdout, "the lab of %s runs %s already\n", l.Cluster, joinNames(nodeNames(l)))
			return nil
		}
		fmt.Fprintf(a.stdout, "starting %s\n", joinNames(stopped))
		startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := lab.StartStopped(startCtx, dir); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "the lab of %s runs %s\n", l.Cluster, joinNames(nodeNames(l)))
		return nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := lab.PrepareStart(stopCtx, dir); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "starting %s\n", joinNames(nodeNames(l)))
	if err := a.startSupervisor(ctx, dir); err != nil {
		return fmt.Errorf("%w; chalklab status shows the lab", err)
	}
	fmt.Fprintf(a.stdout, "the lab of %s runs %s\n", l.Cluster, joinNames(nodeNames(l)))
	return nil
}

// registerCluster registers --cluster, which names the lab of a command.
func registerCluster(cmd *cobra.Command, cluster *string, usage string) {
	cmd.Flags().StringVar(cluster, "cluster", "", usage)
}
