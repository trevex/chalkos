package storage

import "testing"

// The expected types are what the cluster definition renders (see the golden manifest).
func TestPartitionType(t *testing.T) {
	for label, want := range map[string]string{
		"var":      "65f335d7-a1f7-f6df-b954-a97d9a5db9e6",
		"longhorn": "4e6bd13b-2e2a-03b6-3fc9-fc66eae45cba",
	} {
		if got := PartitionType(label); got != want {
			t.Errorf("PartitionType(%q) = %s, want %s", label, got, want)
		}
	}
}
