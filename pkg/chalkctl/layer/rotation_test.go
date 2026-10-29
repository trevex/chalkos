package layer

import (
	"errors"
	"testing"
)

// EachNode runs every node after one failed and names each that did.
func TestEachNode(t *testing.T) {
	var ran []string
	err := EachNode([]string{"a", "b", "c"}, func(name string) error {
		ran = append(ran, name)
		if name != "b" {
			return errors.New("down")
		}
		return nil
	})
	if len(ran) != 3 || err == nil || err.Error() != "a: down; c: down" {
		t.Errorf("ran %v, err %v", ran, err)
	}
}
