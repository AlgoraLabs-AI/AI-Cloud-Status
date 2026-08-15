package monitor

import "testing"

func TestLossWindowEmpty(t *testing.T) {
	w := NewLossWindow(5)
	if got := w.LossPercent(); got != 0 {
		t.Errorf("empty window loss = %v, want 0", got)
	}
	if got := w.Count(); got != 0 {
		t.Errorf("empty window count = %d, want 0", got)
	}
}

func TestLossWindowAllSuccess(t *testing.T) {
	w := NewLossWindow(4)
	for i := 0; i < 4; i++ {
		w.Add(true)
	}
	if got := w.LossPercent(); got != 0 {
		t.Errorf("loss = %v, want 0", got)
	}
}

func TestLossWindowAllFail(t *testing.T) {
	w := NewLossWindow(4)
	for i := 0; i < 4; i++ {
		w.Add(false)
	}
	if got := w.LossPercent(); got != 100 {
		t.Errorf("loss = %v, want 100", got)
	}
}

func TestLossWindowMixed(t *testing.T) {
	w := NewLossWindow(4)
	w.Add(true)
	w.Add(false)
	w.Add(true)
	w.Add(false)
	if got := w.LossPercent(); got != 50 {
		t.Errorf("loss = %v, want 50", got)
	}
}

func TestLossWindowEviction(t *testing.T) {
	w := NewLossWindow(3)
	// Old failures should be evicted by newer successes.
	w.Add(false)
	w.Add(false)
	w.Add(false)
	w.Add(true)
	w.Add(true)
	w.Add(true)
	if got := w.Count(); got != 3 {
		t.Fatalf("count = %d, want 3 (window capped)", got)
	}
	if got := w.LossPercent(); got != 0 {
		t.Errorf("loss = %v, want 0 after eviction", got)
	}
}

func TestClassifyLoss(t *testing.T) {
	cases := []struct {
		loss float64
		want ConnLevel
	}{
		{0, ConnOK},
		{10, ConnOK}, // boundary: not above 10
		{10.1, ConnDegraded},
		{50, ConnDegraded}, // boundary: not above 50
		{50.1, ConnBad},
		{99.9, ConnBad},
		{100, ConnDown},
	}
	for _, c := range cases {
		if got := ClassifyLoss(c.loss); got != c.want {
			t.Errorf("ClassifyLoss(%v) = %v, want %v", c.loss, got, c.want)
		}
	}
}
