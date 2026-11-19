package model

import "testing"

func TestComponentLabel(t *testing.T) {
	cases := map[Component]string{
		DaemonSetSensor: "DaemonSet Sensor",
		KAC:             "KAC",
		IAR:             "IAR",
		Injector:        "Container Sensor Injector",
		Operator:        "Falcon Operator",
		Self:            "Falcon Footprint",
	}
	for c, want := range cases {
		if got := c.Label(); got != want {
			t.Errorf("%s.Label() = %q, want %q", c, got, want)
		}
	}
	if Component("x").Label() != "x" {
		t.Error("unknown component should label as itself")
	}
}

func TestFalconComponents(t *testing.T) {
	for _, c := range FalconComponents {
		if c == Self {
			t.Fatal("FalconComponents must not include Self")
		}
	}
	if len(FalconComponents) != 5 {
		t.Fatalf("want 5 falcon components, got %d", len(FalconComponents))
	}
}
