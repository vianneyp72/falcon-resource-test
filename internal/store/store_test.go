package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "fp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// t0 is aligned to 5 minutes so bucket math in tests is exact.
var t0 = time.Unix(1_700_000_100, 0)

func TestSamplesRoundTrip(t *testing.T) {
	s := open(t)
	in := []model.Sample{
		{TS: t0, Node: "n1", Namespace: "falcon-system", Pod: "p1", Container: "falcon-node-sensor", Component: model.DaemonSetSensor, CPUm: 40, MemBytes: 300 << 20},
		{TS: t0.Add(15 * time.Second), Node: "n1", Namespace: "falcon-system", Pod: "p1", Container: "falcon-node-sensor", Component: model.DaemonSetSensor, CPUm: 60, MemBytes: 310 << 20},
	}
	if err := s.InsertSamples(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Samples(t0, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].CPUm != 60 || got[1].CPUMaxm != 60 || got[0].Component != model.DaemonSetSensor {
		t.Fatalf("unexpected samples: %+v", got)
	}
	// range end is exclusive
	got, _ = s.Samples(t0, t0.Add(15*time.Second))
	if len(got) != 1 {
		t.Fatalf("want 1 sample in [t0,t0+15s), got %d", len(got))
	}
}

func TestNodeSamplesAndEvents(t *testing.T) {
	s := open(t)
	if err := s.InsertNodeSamples([]model.NodeSample{{TS: t0, Node: "n1", AllocCPUm: 4000, AllocMem: 16 << 30, UsedCPUm: 800, UsedMem: 4 << 30}}); err != nil {
		t.Fatal(err)
	}
	ns, _ := s.NodeSamples(t0, t0.Add(time.Second))
	if len(ns) != 1 || ns[0].AllocCPUm != 4000 {
		t.Fatalf("node samples: %+v", ns)
	}
	ev := model.Event{TS: t0, Kind: model.EventOOMKill, Namespace: "falcon-system", Object: "p1", Node: "n1", Reason: "OOMKilled", Falcon: true}
	if err := s.InsertEvent(ev); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Events(t0, t0.Add(time.Second))
	if len(evs) != 1 || !evs[0].Falcon || evs[0].Kind != model.EventOOMKill {
		t.Fatalf("events: %+v", evs)
	}
}

func TestSnapshotsDedupeAndWindow(t *testing.T) {
	s := open(t)
	a := []model.DaemonSetState{{Namespace: "falcon-system", Name: "falcon-sensor", Desired: 3, Ready: 3}}
	b := []model.DaemonSetState{{Namespace: "falcon-system", Name: "falcon-sensor", Desired: 3, Ready: 2}}
	s.PutSnapshot(t0, model.SnapDaemonSets, a)
	s.PutSnapshot(t0.Add(time.Minute), model.SnapDaemonSets, a) // identical: skipped
	s.PutSnapshot(t0.Add(2*time.Minute), model.SnapDaemonSets, b)

	// Window starting after the first snapshot still includes it as the
	// state in effect at `from`.
	snaps, err := s.SnapshotsBetween(model.SnapDaemonSets, t0.Add(30*time.Second), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || !snaps[0].TS.Equal(t0) {
		t.Fatalf("want [t0, t0+2m], got %+v", snaps)
	}
	var got []model.DaemonSetState
	if err := snaps[1].Decode(&got); err != nil || got[0].Ready != 2 {
		t.Fatalf("decode: %v %+v", err, got)
	}
	latest, ok, _ := s.LatestSnapshot(model.SnapDaemonSets)
	if !ok || !latest.TS.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("latest: %+v", latest)
	}
}
