package monitor

import (
	"testing"
	"time"
)

func TestEngineAvgTargetLossAndReset(t *testing.T) {
	e := NewEngine(
		[]Target{{ID: "a", Host: "a"}, {ID: "b", Host: "b"}},
		4, time.Second, NotifierFunc(func(_, _ string) {}),
	)
	// Target a loses 2 of 4 probes (50%); target b loses none (0%). Mean = 25%.
	// (InternetLossPercent, by contrast, would be 0 here — b always answered — which
	// is exactly why the escalator uses the average, not the aggregate.)
	for _, ok := range []bool{true, false, true, false} {
		e.perTarget["a"].Add(ok)
	}
	for i := 0; i < 4; i++ {
		e.perTarget["b"].Add(true)
	}
	if got := e.AvgTargetLossPercent(); got != 25 {
		t.Errorf("AvgTargetLossPercent = %v, want 25", got)
	}

	e.ResetLoss()
	if got := e.AvgTargetLossPercent(); got != 0 {
		t.Errorf("after ResetLoss = %v, want 0", got)
	}
}
