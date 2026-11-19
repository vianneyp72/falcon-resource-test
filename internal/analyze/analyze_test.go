package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-cloud-security-labs/tools/falcon-footprint/internal/model"
)

func fixture() Input {
	in := Input{From: t0, To: t0.Add(15 * time.Minute), Interval: 15 * time.Second, KACPresent: true}
	for i := 0; i < 60; i++ {
		ts := t0.Add(time.Duration(i) * 15 * time.Second)
		mk := func(node, pod string, c model.Component, cpu, mem int64) model.Sample {
			return model.Sample{TS: ts, Node: node, Namespace: "ns", Pod: pod, Container: "c", Component: c, CPUm: cpu, MemBytes: mem, CPUMaxm: cpu, MemMax: mem}
		}
		in.Samples = append(in.Samples,
			mk("n1", "s1", model.DaemonSetSensor, 20, 300<<20),
			mk("n2", "s2", model.DaemonSetSensor, 60, 400<<20),
			mk("n1", "k1", model.KAC, 12, 100<<20),
			mk("n1", "fp", model.Self, 5, 30<<20))
		in.NodeSamples = append(in.NodeSamples,
			model.NodeSample{TS: ts, Node: "n1", AllocCPUm: 2000, AllocMem: 8 << 30, UsedCPUm: 100, UsedMem: 2 << 30},
			model.NodeSample{TS: ts, Node: "n2", AllocCPUm: 2000, AllocMem: 8 << 30, UsedCPUm: 100, UsedMem: 2 << 30})
	}
	in.DaemonSets = []TimedDaemonSets{{TS: t0, State: []model.DaemonSetState{{Namespace: "falcon-system", Name: "falcon-sensor", Desired: 2, Ready: 2}}}}
	in.Webhooks = []model.WebhookState{{Name: "validating.falcon-kac", Kind: "validating", FailurePolicy: "Ignore", TimeoutSeconds: 10}}
	in.Specs = []model.ContainerSpec{
		{Component: model.DaemonSetSensor, Container: "c", Version: "7.20.0", Backend: "bpf", ReqCPUm: 250, ReqMem: 500 << 20},
		{Component: model.KAC, Container: "c", Version: "7.39.0", ReqCPUm: 50, ReqMem: 128 << 20, LimMem: 256 << 20},
	}
	return in
}

func TestAnalyzeHeadlineAndCluster(t *testing.T) {
	r := Analyze(fixture())
	// falcon cpu per tick = 92m of 4000m = 2.3%; self is excluded
	if r.Cluster.CPUPct < 2.29 || r.Cluster.CPUPct > 2.31 {
		t.Fatalf("cluster cpu pct = %v", r.Cluster.CPUPct)
	}
	want := "Over 15 minutes on 2 nodes, Falcon used 2.3% of cluster CPU and 4.9% of memory, with 0 restarts and 2/2 node coverage."
	if r.Headline != want {
		t.Fatalf("headline:\n got %q\nwant %q", r.Headline, want)
	}
	if r.Self.CPU.Avg != 5 {
		t.Fatalf("self reported separately: %+v", r.Self)
	}
}

func TestAnalyzeComponentsAndNodes(t *testing.T) {
	r := Analyze(fixture())
	if len(r.Components) != 2 || r.Components[0].Component != model.DaemonSetSensor {
		t.Fatalf("components in display order: %+v", r.Components)
	}
	ds := r.Components[0]
	if ds.Pods != 2 || ds.CPU.Avg != 80 || ds.ReqCPUm != 500 {
		t.Fatalf("sensor stats (reserved = request x pods): %+v", ds)
	}
	if r.Nodes[0].Node != "n2" || r.Nodes[0].CPUShareP95 != 3 {
		t.Fatalf("worst node first: %+v", r.Nodes)
	}
}

func TestAnalyzeChecksAndMethodology(t *testing.T) {
	r := Analyze(fixture())
	got := map[string]Status{}
	for _, c := range r.Checks {
		got[c.ID] = c.Status
	}
	want := map[string]Status{"stable": Pass, "lane": Pass, "crowd": Pass, "coverage": Pass, "admission": Pass, "sensor": NotMeasured}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("check %s = %s want %s", id, got[id], s)
		}
	}
	if !r.Method.Idle || !strings.Contains(strings.Join(r.Method.Notes, " "), "Idle-cluster baseline") {
		t.Fatalf("5%% cluster cpu should be flagged idle: %+v", r.Method)
	}
	if r.Method.Gaps != 0 {
		t.Fatalf("no gaps expected: %v", r.Method.Gaps)
	}
}

func TestAnalyzeGaps(t *testing.T) {
	in := fixture()
	var keepS []model.Sample
	var keepN []model.NodeSample
	for _, s := range in.Samples {
		if s.TS.Sub(t0) < 5*time.Minute || s.TS.Sub(t0) >= 7*time.Minute {
			keepS = append(keepS, s)
		}
	}
	for _, n := range in.NodeSamples {
		if n.TS.Sub(t0) < 5*time.Minute || n.TS.Sub(t0) >= 7*time.Minute {
			keepN = append(keepN, n)
		}
	}
	in.Samples, in.NodeSamples = keepS, keepN
	r := Analyze(in)
	if r.Method.Gaps < 115*time.Second || r.Method.Gaps > 125*time.Second {
		t.Fatalf("want ~2m gap, got %v", r.Method.Gaps)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	r := Analyze(Input{From: t0, To: t0.Add(time.Minute), Interval: 15 * time.Second})
	if !strings.Contains(r.Headline, "No Falcon usage") {
		t.Fatalf("empty headline: %q", r.Headline)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Minute: "5 minutes", time.Hour: "1 hour", 90 * time.Minute: "1 hour 30 minutes",
		24 * time.Hour: "24 hours", 30 * time.Second: "30 seconds",
	} {
		if got := HumanDuration(d); got != want {
			t.Errorf("%v => %q want %q", d, got, want)
		}
	}
}
