package testwait

import (
	"testing"
	"time"
)

func TestBubbleStartsAtTheRealTime(t *testing.T) {
	before := time.Now()
	Bubble(t, func(t *testing.T) {
		if d := time.Since(before); d < 0 || d > time.Second {
			t.Errorf("the bubble's clock is %v away from the real time", d)
		}
		time.Sleep(time.Hour)
	})
	if d := time.Since(before); d > time.Minute {
		t.Errorf("an hour's sleep in the bubble took %v", d)
	}
}
