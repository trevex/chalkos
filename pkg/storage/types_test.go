package storage

import "testing"

// The expected types are those of the partitions installed nodes hold, so they never change.
func TestPartitionType(t *testing.T) {
	for label, want := range map[string]string{
		// STATE's, which the image's disk module derives the same way.
		"state":    "480b7842-1236-1abb-0339-e5503b43122a",
		"var":      "65f335d7-a1f7-f6df-b954-a97d9a5db9e6",
		"longhorn": "4e6bd13b-2e2a-03b6-3fc9-fc66eae45cba",
	} {
		if got := PartitionType(label); got != want {
			t.Errorf("PartitionType(%q) = %s, want %s", label, got, want)
		}
	}
}

func TestUUIDOf(t *testing.T) {
	// The partition type of VAR, as the image derives it too.
	if got := UUIDOf("chalkos-partition-type:var"); got != "65f335d7-a1f7-f6df-b954-a97d9a5db9e6" {
		t.Errorf("UUIDOf = %s", got)
	}
}
