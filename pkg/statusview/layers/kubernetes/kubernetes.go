// Package kubernetes writes the Kubernetes layer's section of a node's status, which chalkctl
// status prints and the console's Kubernetes tab shows.
package kubernetes

import (
	"cmp"
	"fmt"
	"io"
	"time"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/statusview"
)

// Write writes the node's part in its cluster, the versions it runs, the revoked nodes a control
// plane lists and the node's pods that do not run.
func Write(w io.Writer, k *kubernetesv1.StatusResponse) error {
	for _, line := range StatusLines(k) {
		fmt.Fprintln(w, line)
	}
	t := statusview.Table(w)
	for _, line := range revokedLines(k) {
		fmt.Fprintln(t, line)
	}
	for _, line := range podLines(k) {
		fmt.Fprintln(t, line)
	}
	return t.Flush()
}

// StatusLines are the lines of the node's part in its cluster and of the versions it runs.
func StatusLines(k *kubernetesv1.StatusResponse) []string {
	return []string{kubernetesLine(k), versionsLine(k)}
}

// WriteRevoked writes the revoked nodes a control plane lists, or why it could not read them.
func WriteRevoked(w io.Writer, k *kubernetesv1.StatusResponse) error {
	t := statusview.Table(w)
	for _, line := range revokedLines(k) {
		fmt.Fprintln(t, line)
	}
	return t.Flush()
}

// MorePods is how many of the node's pods that do not run the status counts beyond those it
// lists.
func MorePods(k *kubernetesv1.StatusResponse) int {
	return max(0, int(k.GetPodsNotRunningTotal())-len(k.GetPodsNotRunning()))
}

// kubernetesLine is the status line of the node's Kubernetes side. A Ready condition that is not
// True comes with its reason and message.
func kubernetesLine(k *kubernetesv1.StatusResponse) string {
	line := fmt.Sprintf("kubernetes %s: %s", k.Kind, k.State)
	if k.NodeReady != "" {
		line += ", node ready: " + k.NodeReady
		if why := readyWhy(k); k.NodeReady != "True" && why != "" {
			line += " (" + why + ")"
		}
	}
	if k.Vip != "" {
		line += ", vip " + k.Vip
	}
	if k.ControlPlane != "" {
		line += ", control plane " + k.ControlPlane
	}
	return line
}

// readyWhy is the reason and the message of the Node's Ready condition, joined by a colon when
// both are set.
func readyWhy(k *kubernetesv1.StatusResponse) string {
	if k.NodeReadyReason != "" && k.NodeReadyMessage != "" {
		return k.NodeReadyReason + ": " + k.NodeReadyMessage
	}
	return k.NodeReadyReason + k.NodeReadyMessage
}

// versionsLine is the status line of the versions the node runs: the API server its config names
// and its image's kubelet, and on a control plane whether its static pods run what the config
// names.
func versionsLine(k *kubernetesv1.StatusResponse) string {
	line := fmt.Sprintf("kubernetes versions: API server %s, kubelet %s", k.GetApiServerVersion(), k.GetKubeletVersion())
	if p := k.GetControlPlanePods(); p != "" {
		line += ", control-plane pods " + p
	}
	return line
}

// revokedLines are the status lines of the nodes a control plane lists as revoked, or of why
// it could not read them.
func revokedLines(k *kubernetesv1.StatusResponse) []string {
	if k.RevokedNodesError != "" {
		return []string{"revoked nodes unknown: " + k.RevokedNodesError}
	}
	if len(k.RevokedNodes) == 0 {
		return nil
	}
	lines := []string{"revoked nodes:"}
	for _, n := range k.RevokedNodes {
		lines = append(lines, "  "+n.Name+"\t"+RevokedSince(n))
	}
	return lines
}

// podLines are the status lines of the node's pods that do not run, with how many the status
// leaves out, or of why they could not be read.
func podLines(k *kubernetesv1.StatusResponse) []string {
	if k.PodsError != "" {
		return []string{"pods unknown: " + k.PodsError}
	}
	if len(k.PodsNotRunning) == 0 {
		return nil
	}
	lines := []string{"pods not running:"}
	for _, p := range k.PodsNotRunning {
		lines = append(lines, fmt.Sprintf("  %s/%s\t%s\t%s", p.Namespace, p.Name, p.Phase, cmp.Or(p.Reason, "-")))
	}
	if n := MorePods(k); n > 0 {
		lines = append(lines, fmt.Sprintf("  and %d more", n))
	}
	return lines
}

// RevokedSince says when a node was revoked.
func RevokedSince(n *kubernetesv1.RevokedNode) string {
	if n.RevokedAt == nil {
		return "at an unknown time"
	}
	return "since " + n.RevokedAt.AsTime().UTC().Format(time.RFC3339)
}
