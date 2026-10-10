package lab

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ChalkdPort is the port chalkd serves on in the guest.
const ChalkdPort = 50000

// Lab is a lab cluster as its state directory records it: a VM for each of its nodes, connected
// by a switch, with ports on 127.0.0.1 that reach chalkd and the API servers.
type Lab struct {
	Cluster string `json:"cluster"`
	// FirmwareCode is the OVMF code every VM runs, and FirmwareVars the variable store with the
	// lab's Secure Boot keys enrolled, which each VM starts from.
	FirmwareCode  string         `json:"firmwareCode"`
	FirmwareVars  string         `json:"firmwareVars"`
	Nodes         []LabNode      `json:"nodes"`
	GuestForwards []GuestForward `json:"guestForwards,omitempty"`
}

// LabNode is the VM of a node of a lab.
type LabNode struct {
	Name string `json:"name"`
	Role string `json:"role"`
	// Kind is the node's Kubernetes kind: controlplane, worker, or empty.
	Kind string `json:"kind,omitempty"`
	// MAC is the address of the VM's NIC on the lab network.
	MAC      string `json:"mac"`
	MemoryMB int    `json:"memoryMB"`
	CPUs     int    `json:"cpus"`
	// ChalkdPort is the port on 127.0.0.1 that reaches the node's chalkd, and APIPort the one that
	// reaches its API server; zero on a node without one.
	ChalkdPort int `json:"chalkdPort"`
	APIPort    int `json:"apiPort,omitempty"`
}

const labFile = "lab.json"

// StateRoot is where chalklab keeps its labs: $XDG_STATE_HOME/chalklab, or
// ~/.local/state/chalklab.
func StateRoot() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find the state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME is %q, not an absolute path", base)
	}
	return filepath.Join(base, "chalklab"), nil
}

// StateDir is the state directory of the lab of a cluster.
func StateDir(cluster string) (string, error) {
	root, err := StateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, cluster), nil
}

// ReadLab reads the lab of a state directory.
func ReadLab(dir string) (*Lab, error) {
	data, err := os.ReadFile(filepath.Join(dir, labFile))
	if err != nil {
		return nil, err
	}
	var l Lab
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, labFile), err)
	}
	return &l, nil
}

// Write records the lab in its state directory.
func (l *Lab) Write(dir string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, labFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, labFile))
}

// Node returns the lab's node of the name.
func (l *Lab) Node(name string) (LabNode, error) {
	for _, n := range l.Nodes {
		if n.Name == name {
			return n, nil
		}
	}
	var names []string
	for _, n := range l.Nodes {
		names = append(names, n.Name)
	}
	return LabNode{}, fmt.Errorf("the lab of %s has no node %s; its nodes are %v", l.Cluster, name, names)
}

// VMConfig is the VM of a node of the lab whose state directory is dir: its disk, firmware
// variables, TPM state and console log are in dir/<node>, and the switch's sockets in dir/switch.
func (l *Lab) VMConfig(dir string, n LabNode) VMConfig {
	forwards := []Forward{{Host: n.ChalkdPort, Guest: ChalkdPort}}
	if n.APIPort != 0 {
		forwards = append(forwards, Forward{Host: n.APIPort, Guest: 6443})
	}
	return VMConfig{
		Name:          n.Name,
		Dir:           filepath.Join(dir, n.Name),
		FirmwareCode:  l.FirmwareCode,
		FirmwareVars:  l.FirmwareVars,
		Disks:         []Disk{{Path: filepath.Join(dir, n.Name, "disk.qcow2")}},
		MemoryMB:      n.MemoryMB,
		CPUs:          n.CPUs,
		TPM:           true,
		Forwards:      forwards,
		GuestForwards: l.GuestForwards,
		Switch:        filepath.Join(dir, "switch"),
		MAC:           n.MAC,
		GuestAgent:    true,
	}
}

// Running reports whether the VM of the configuration runs.
func Running(c VMConfig) bool {
	_, ok := runningPID(c.pidPath(), c.Dir)
	return ok
}

// socketPaths are the Unix sockets of a lab, which must fit the 107 bytes a socket path holds.
func (l *Lab) socketPaths(dir string) []string {
	// A VM's side of the switch is a socket named after its PID and a counter.
	paths := []string{filepath.Join(dir, "switch", "ctl"), filepath.Join(dir, "switch", ".0000000-00000")}
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		paths = append(paths, c.qmpPath(), c.GuestAgentSocket(), filepath.Join(c.tpmDir(), "swtpm.sock"))
	}
	return paths
}

// CheckSockets fails when a socket of the lab would not fit a Unix socket's path.
func (l *Lab) CheckSockets(dir string) error {
	for _, p := range l.socketPaths(dir) {
		if len(p) > 107 {
			return errors.New("the lab's sockets, such as " + p + ", would be longer than a Unix socket's path may be; set XDG_STATE_HOME to a shorter directory")
		}
	}
	return nil
}
