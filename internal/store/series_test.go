package store

import (
	"testing"
	"time"

	"github.com/vianneyp72/falcon-cloud-security-labs/tools/falcon-footprint/internal/model"
)

// two sensor pods + one KAC pod, every 15s for 10 minutes
func seed(t *testing.T, s *Store) {
	t.Helper()
	var in []model.Sample
	var nodes []model.NodeSample
	for i := 0; i < 40; i++ {
		ts := t0.Add(time.Duration(i) * 15 * time.Second)
		in = append(in,
			model.Sample{TS: ts, Node: "n1", Namespace: "falcon-system", Pod: "s1", Container: "c", Component: model.DaemonSetSensor, CPUm: 10, MemBytes: 100},
			model.Sample{TS: ts, Node: "n2", Namespace: "falcon-system", Pod: "s2", Container: "c", Component: model.DaemonSetSensor, CPUm: 30, MemBytes: 300},
			model.Sample{TS: ts, Node: "n1", Namespace: "falcon-kac", Pod: "k1", Container: "c", Component: model.KAC, CPUm: int64(i), MemBytes: 50},
		)
		nodes = append(nodes,
			model.NodeSample{TS: ts, Node: "n1", AllocCPUm: 2000, AllocMem: 1000, UsedCPUm: 500, UsedMem: 400},
			model.NodeSample{TS: ts, Node: "n2", AllocCPUm: 2000, AllocMem: 1000, UsedCPUm: 300, UsedMem: 200})
	}
	if err := s.InsertSamples(in); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertNodeSamples(nodes); err != nil {
		t.Fatal(err)
	}
}

func TestComponentSeries(t *testing.T) {
	s := open(t)
	seed(t, s)
	got, err := s.ComponentSeries(t0, t0.Add(10*time.Minute), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	ds := got[model.DaemonSetSensor]
	if len(ds) != 10 {
		t.Fatalf("want 10 one-minute points, got %d", len(ds))
	}
	// per tick the two sensor pods sum to 40m / 400B
	if ds[0].CPUm != 40 || ds[0].MemBytes != 400 {
		t.Fatalf("sensor point: %+v", ds[0])
	}
	// filtered to one node
	got, _ = s.ComponentSeries(t0, t0.Add(10*time.Minute), time.Minute, "n2")
	if got[model.DaemonSetSensor][0].CPUm != 30 || len(got[model.KAC]) != 0 {
		t.Fatalf("node filter: %+v", got)
	}
}

func TestClusterSeries(t *testing.T) {
	s := open(t)
	seed(t, s)
	got, err := s.ClusterSeries(t0, t0.Add(10*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].AllocCPUm != 4000 || got[0].UsedCPUm != 800 {
		t.Fatalf("cluster series: %+v", got)
	}
}

func TestBucketedSamples(t *testing.T) {
	s := open(t)
	seed(t, s)
	got, err := s.BucketedSamples(t0, t0.Add(10*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 2 buckets x 3 pods
	if len(got) != 6 {
		t.Fatalf("want 6 rows, got %d", len(got))
	}
	for _, x := range got {
		if x.Component == model.KAC && x.TS.Equal(t0.Add(5*time.Minute)) && (x.CPUm != 29 || x.CPUMaxm != 39) {
			t.Fatalf("KAC bucket 2: avg %d max %d, want 29/39", x.CPUm, x.CPUMaxm)
		}
	}
	ns, err := s.BucketedNodeSamples(t0, t0.Add(10*time.Minute), 5*time.Minute)
	if err != nil || len(ns) != 4 || ns[0].AllocCPUm != 2000 {
		t.Fatalf("bucketed nodes: %+v %v", ns, err)
	}
}

func TestRollupKeepsAvgAndMax(t *testing.T) {
	s := open(t)
	seed(t, s)
	if err := s.Rollup(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Samples(t0, t0.Add(time.Hour))
	var kacMax int64
	for _, x := range rows {
		if x.TS.Unix()%300 != 0 {
			t.Fatalf("raw row survived rollup: ts %d", x.TS.Unix())
		}
		if x.Component == model.KAC && x.CPUMaxm > kacMax {
			kacMax = x.CPUMaxm
		}
	}
	// KAC cpu ran 0..39; second bucket holds 20..39
	if kacMax != 39 {
		t.Fatalf("rollup max = %d, want 39", kacMax)
	}
	ns, _ := s.NodeSamples(t0, t0.Add(time.Hour))
	if len(ns) != 4 || ns[0].AllocCPUm != 2000 {
		t.Fatalf("node rollup: %+v", ns)
	}
}

func TestExpire(t *testing.T) {
	s := open(t)
	seed(t, s)
	s.InsertEvent(model.Event{TS: t0, Kind: model.EventRestart})
	s.PutSnapshot(t0, model.SnapDaemonSets, []int{1})
	s.PutSnapshot(t0.Add(time.Minute), model.SnapDaemonSets, []int{2})
	if err := s.Expire(t0.Add(5 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.Samples(t0, t0.Add(5*time.Minute))
	evs, _ := s.Events(t0, t0.Add(time.Hour))
	if len(raw) != 0 || len(evs) != 0 {
		t.Fatalf("expired data still present: %d samples %d events", len(raw), len(evs))
	}
	// latest snapshot per kind survives expiry so current state is never lost
	if _, ok, _ := s.LatestSnapshot(model.SnapDaemonSets); !ok {
		t.Fatal("latest snapshot was expired")
	}
	if n, err := s.SizeBytes(); err != nil || n <= 0 {
		t.Fatalf("SizeBytes: %d %v", n, err)
	}
}
