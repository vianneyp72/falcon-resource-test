package analyze

import (
	"fmt"
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

type Input struct {
	From, To     time.Time
	Interval     time.Duration
	Samples      []model.Sample
	NodeSamples  []model.NodeSample
	Events       []model.Event
	DaemonSets   []TimedDaemonSets
	Webhooks     []model.WebhookState  // latest Falcon webhook state
	Specs        []model.ContainerSpec // latest Falcon container specs
	Sensors      []model.SensorHost
	SensorsKnown bool // Falcon API enabled and answered
	KACPresent   bool
}

type ComponentStat struct {
	Component model.Component `json:"component"`
	Label     string          `json:"label"`
	Pods      int             `json:"pods"`
	CPU       Stat            `json:"cpu"`       // millicores, summed across pods
	Mem       Stat            `json:"mem"`       // bytes, summed across pods
	ReqCPUm   int64           `json:"req_cpu_m"` // reserved = per-pod request x pods
	ReqMem    int64           `json:"req_mem"`
	LimCPUm   int64           `json:"lim_cpu_m"` // per pod, 0 = none
	LimMem    int64           `json:"lim_mem"`
}

type NodeStat struct {
	Node        string  `json:"node"`
	CPU         Stat    `json:"cpu"`
	Mem         Stat    `json:"mem"`
	CPUShareP95 float64 `json:"cpu_share_p95"` // % of node allocatable
	MemShareP95 float64 `json:"mem_share_p95"`
}

type Cluster struct {
	Nodes          int     `json:"nodes"`
	AllocCPUm      int64   `json:"alloc_cpu_m"`
	AllocMem       int64   `json:"alloc_mem"`
	CPUPct         float64 `json:"cpu_pct"` // avg Falcon used / allocatable
	MemPct         float64 `json:"mem_pct"`
	ReservedCPUPct float64 `json:"reserved_cpu_pct"`
	ReservedMemPct float64 `json:"reserved_mem_pct"`
	CPU            Stat    `json:"cpu"`
	Mem            Stat    `json:"mem"`
}

type SelfStat struct {
	CPU Stat `json:"cpu"`
	Mem Stat `json:"mem"`
}

type Methodology struct {
	Window        time.Duration `json:"window"`
	ClusterCPUPct float64       `json:"cluster_cpu_pct"` // all workloads
	Idle          bool          `json:"idle"`
	Gaps          time.Duration `json:"gaps"`
	Notes         []string      `json:"notes"`
}

type Report struct {
	From       time.Time             `json:"from"`
	To         time.Time             `json:"to"`
	Headline   string                `json:"headline"`
	Cluster    Cluster               `json:"cluster"`
	Components []ComponentStat       `json:"components"`
	Nodes      []NodeStat            `json:"nodes"`
	Checks     []Check               `json:"checks"`
	Config     []model.ContainerSpec `json:"config"`
	Self       SelfStat              `json:"self"`
	Method     Methodology           `json:"method"`
}

const idleThresholdPct = 10

const undercountNote = "Part of the sensor's work runs inside the system calls of the workloads it watches and is billed to those workloads, so pod metrics slightly undercount Falcon's total cost."

type nodeKey struct {
	ts   int64
	node string
}

func Analyze(in Input) Report {
	r := Report{From: in.From, To: in.To}
	window := in.To.Sub(in.From)

	nodeAt := map[nodeKey]model.NodeSample{}
	clusterAlloc := map[int64][2]int64{} // ts -> alloc cpu, mem
	clusterUsed := map[int64][2]int64{}
	nodes := map[string]bool{}
	for _, n := range in.NodeSamples {
		ts := n.TS.Unix()
		nodeAt[nodeKey{ts, n.Node}] = n
		a, u := clusterAlloc[ts], clusterUsed[ts]
		clusterAlloc[ts] = [2]int64{a[0] + n.AllocCPUm, a[1] + n.AllocMem}
		clusterUsed[ts] = [2]int64{u[0] + n.UsedCPUm, u[1] + n.UsedMem}
		nodes[n.Node] = true
	}

	type agg struct{ cpu, mem, cpuMax, memMax int64 }
	compTick := map[model.Component]map[int64]*agg{}
	nodeTick := map[string]map[int64]*agg{}
	falconTick := map[int64]*agg{}
	selfTick := map[int64]*agg{}
	podsAt := map[model.Component]map[int64]map[string]bool{}
	add := func(m map[int64]*agg, ts int64, s model.Sample) {
		a := m[ts]
		if a == nil {
			a = &agg{}
			m[ts] = a
		}
		a.cpu += s.CPUm
		a.mem += s.MemBytes
		a.cpuMax += s.CPUMaxm
		a.memMax += s.MemMax
	}
	for _, s := range in.Samples {
		ts := s.TS.Unix()
		if s.Component == model.Self {
			add(selfTick, ts, s)
			continue
		}
		if compTick[s.Component] == nil {
			compTick[s.Component] = map[int64]*agg{}
			podsAt[s.Component] = map[int64]map[string]bool{}
		}
		add(compTick[s.Component], ts, s)
		if podsAt[s.Component][ts] == nil {
			podsAt[s.Component][ts] = map[string]bool{}
		}
		podsAt[s.Component][ts][s.Pod] = true
		if nodeTick[s.Node] == nil {
			nodeTick[s.Node] = map[int64]*agg{}
		}
		add(nodeTick[s.Node], ts, s)
		add(falconTick, ts, s)
	}

	summarize := func(m map[int64]*agg) (Stat, Stat) {
		var c, cm, mm, mmx []int64
		for _, a := range m {
			c, cm = append(c, a.cpu), append(cm, a.cpuMax)
			mm, mmx = append(mm, a.mem), append(mmx, a.memMax)
		}
		return Summarize(c, cm), Summarize(mm, mmx)
	}

	// Components, in display order.
	specBy := map[model.Component][]model.ContainerSpec{}
	for _, s := range in.Specs {
		specBy[s.Component] = append(specBy[s.Component], s)
	}
	var reservedCPU, reservedMem int64
	for _, comp := range model.FalconComponents {
		m := compTick[comp]
		if m == nil {
			continue
		}
		cs := ComponentStat{Component: comp, Label: comp.Label()}
		cs.CPU, cs.Mem = summarize(m)
		var latest int64
		for ts := range podsAt[comp] {
			if ts > latest {
				latest = ts
			}
		}
		cs.Pods = len(podsAt[comp][latest])
		for _, sp := range specBy[comp] {
			cs.ReqCPUm += sp.ReqCPUm
			cs.ReqMem += sp.ReqMem
			cs.LimCPUm += sp.LimCPUm
			cs.LimMem += sp.LimMem
		}
		cs.ReqCPUm *= int64(cs.Pods)
		cs.ReqMem *= int64(cs.Pods)
		reservedCPU += cs.ReqCPUm
		reservedMem += cs.ReqMem
		r.Components = append(r.Components, cs)
	}

	// Per-node, with share of node allocatable per tick.
	falconNodeShare := map[string]float64{}
	for node, m := range nodeTick {
		ns := NodeStat{Node: node}
		ns.CPU, ns.Mem = summarize(m)
		var cpuShare, memShare []int64 // basis points, for integer percentiles
		for ts, a := range m {
			n, ok := nodeAt[nodeKey{ts, node}]
			if !ok || n.AllocCPUm == 0 || n.AllocMem == 0 {
				continue
			}
			cpuShare = append(cpuShare, a.cpu*10000/n.AllocCPUm)
			memShare = append(memShare, a.mem*10000/n.AllocMem)
		}
		ns.CPUShareP95 = float64(Percentile(cpuShare, 0.95)) / 100
		ns.MemShareP95 = float64(Percentile(memShare, 0.95)) / 100
		falconNodeShare[node] = ns.CPUShareP95
		r.Nodes = append(r.Nodes, ns)
	}
	sort.Slice(r.Nodes, func(i, j int) bool {
		if r.Nodes[i].CPUShareP95 != r.Nodes[j].CPUShareP95 {
			return r.Nodes[i].CPUShareP95 > r.Nodes[j].CPUShareP95
		}
		return r.Nodes[i].Node < r.Nodes[j].Node
	})

	// Cluster totals: average over ticks that have both Falcon and node data.
	var sumF, sumFM, sumA, sumAM, sumU int64
	var ticks int64
	for ts, f := range falconTick {
		a, ok := clusterAlloc[ts]
		if !ok {
			continue
		}
		sumF += f.cpu
		sumFM += f.mem
		sumA += a[0]
		sumAM += a[1]
		sumU += clusterUsed[ts][0]
		ticks++
	}
	r.Cluster.Nodes = len(nodes)
	r.Cluster.CPU, r.Cluster.Mem = summarize(falconTick)
	if ticks > 0 {
		r.Cluster.AllocCPUm, r.Cluster.AllocMem = sumA/ticks, sumAM/ticks
		r.Cluster.CPUPct = Pct(sumF, sumA)
		r.Cluster.MemPct = Pct(sumFM, sumAM)
		r.Cluster.ReservedCPUPct = Pct(reservedCPU, r.Cluster.AllocCPUm)
		r.Cluster.ReservedMemPct = Pct(reservedMem, r.Cluster.AllocMem)
		r.Method.ClusterCPUPct = Pct(sumU, sumA)
	}
	r.Self.CPU, r.Self.Mem = summarize(selfTick)

	// Checks.
	r.Checks = []Check{
		checkStable(in.Events, len(falconTick) > 0),
		checkLane(in.Specs, falconOnly(in.Samples)),
		checkCrowd(in.Events, falconNodeShare),
		checkCoverage(in.DaemonSets, in.From, in.To),
		checkAdmission(in.Webhooks, in.KACPresent),
		checkSensorHealth(in.Sensors, in.SensorsKnown, in.To),
	}
	r.Config = in.Specs

	// Methodology.
	r.Method.Window = window
	r.Method.Gaps = gaps(in.NodeSamples, in.From, in.To, in.Interval)
	r.Method.Notes = append(r.Method.Notes, fmt.Sprintf("Measured over %s. Cluster CPU (all workloads) averaged %.1f%% of allocatable.", HumanDuration(window), r.Method.ClusterCPUPct))
	if ticks > 0 && r.Method.ClusterCPUPct < idleThresholdPct {
		r.Method.Idle = true
		r.Method.Notes = append(r.Method.Notes, "Idle-cluster baseline: the cluster was mostly idle, and sensor CPU grows with process and system-call activity. Rerun during representative traffic before drawing conclusions.")
	}
	r.Method.Notes = append(r.Method.Notes, undercountNote)
	r.Method.Notes = append(r.Method.Notes, fmt.Sprintf("This tool used %dm CPU and %s memory on average, and is excluded from Falcon's totals.", r.Self.CPU.Avg, MiB(r.Self.Mem.Avg)))
	if r.Method.Gaps > 0 {
		r.Method.Notes = append(r.Method.Notes, fmt.Sprintf("%s of the window has no samples (metrics unavailable). Gaps are not filled in.", r.Method.Gaps))
	}

	r.Headline = headline(r, in)
	return r
}

func falconOnly(in []model.Sample) []model.Sample {
	out := make([]model.Sample, 0, len(in))
	for _, s := range in {
		if s.Component != model.Self {
			out = append(out, s)
		}
	}
	return out
}

// gaps totals missing time: the step between consecutive sample timestamps
// beyond 2x the expected step (5m for rollup rows), plus missing time at
// the edges of the window.
func gaps(ns []model.NodeSample, from, to time.Time, interval time.Duration) time.Duration {
	seen := map[int64]bool{}
	var ts []int64
	for _, n := range ns {
		if !seen[n.TS.Unix()] {
			seen[n.TS.Unix()] = true
			ts = append(ts, n.TS.Unix())
		}
	}
	if len(ts) == 0 {
		return 0
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	step := int64(interval / time.Second)
	var gap int64
	check := func(prev, next, s int64) {
		if d := next - prev; d > 2*s {
			gap += d - s
		}
	}
	check(from.Unix()-step, ts[0], step)
	for i := 1; i < len(ts); i++ {
		s := step
		if ts[i]%300 == 0 && ts[i-1]%300 == 0 && ts[i]-ts[i-1] >= 300 {
			s = 300
		}
		check(ts[i-1], ts[i], s)
	}
	check(ts[len(ts)-1], to.Unix(), step)
	return time.Duration(gap) * time.Second
}

func headline(r Report, in Input) string {
	if len(r.Components) == 0 {
		return fmt.Sprintf("No Falcon usage was recorded over %s.", HumanDuration(r.Method.Window))
	}
	restarts := 0
	for _, e := range in.Events {
		if e.Falcon && e.Kind == model.EventRestart {
			restarts++
		}
	}
	coverage := "no DaemonSet Sensor"
	if n := len(in.DaemonSets); n > 0 {
		var ready, desired int32
		for _, d := range in.DaemonSets[n-1].State {
			ready += d.Ready
			desired += d.Desired
		}
		coverage = fmt.Sprintf("%d/%d node coverage", ready, desired)
	}
	return fmt.Sprintf("Over %s on %s, Falcon used %s of cluster CPU and %s of memory, with %s and %s.",
		HumanDuration(r.Method.Window), plural(r.Cluster.Nodes, "node"), pct1(r.Cluster.CPUPct), pct1(r.Cluster.MemPct), plural(restarts, "restart"), coverage)
}

func pct1(p float64) string {
	if p > 0 && p < 0.1 {
		return "<0.1%"
	}
	return fmt.Sprintf("%.1f%%", p)
}

func HumanDuration(d time.Duration) string {
	unit := func(n int, w string) string {
		if n == 1 {
			return "1 " + w
		}
		return fmt.Sprintf("%d %ss", n, w)
	}
	if d < time.Minute {
		return unit(int(d/time.Second), "second")
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return unit(m, "minute")
	case m == 0:
		return unit(h, "hour")
	default:
		return unit(h, "hour") + " " + unit(m, "minute")
	}
}

// Windows longer than this are analyzed from 5-minute buckets.
const bucketAbove = 2 * time.Hour

// Load reads everything Analyze needs for [from, to) from the store.
// sensorsKnown/kacPresent come from the live collector state.
func Load(s *store.Store, from, to time.Time, interval time.Duration, kacPresent, sensorsKnown bool) (Input, error) {
	in := Input{From: from, To: to, Interval: interval, KACPresent: kacPresent, SensorsKnown: sensorsKnown}
	var err error
	if to.Sub(from) > bucketAbove {
		// Long windows: 5m avg+max buckets keep memory flat (24h x 50 nodes
		// of raw rows would be ~40 MB). Gaps logic already handles 5m spacing.
		if in.Samples, err = s.BucketedSamples(from, to, 5*time.Minute); err != nil {
			return in, err
		}
		if in.NodeSamples, err = s.BucketedNodeSamples(from, to, 5*time.Minute); err != nil {
			return in, err
		}
	} else {
		if in.Samples, err = s.Samples(from, to); err != nil {
			return in, err
		}
		if in.NodeSamples, err = s.NodeSamples(from, to); err != nil {
			return in, err
		}
	}
	if in.Events, err = s.Events(from, to); err != nil {
		return in, err
	}
	snaps, err := s.SnapshotsBetween(model.SnapDaemonSets, from, to)
	if err != nil {
		return in, err
	}
	for _, sn := range snaps {
		var st []model.DaemonSetState
		if err := sn.Decode(&st); err == nil {
			in.DaemonSets = append(in.DaemonSets, TimedDaemonSets{TS: sn.TS, State: st})
		}
	}
	latest := func(kind model.SnapshotKind, v any) error {
		snaps, err := s.SnapshotsBetween(kind, to, to)
		if err != nil || len(snaps) == 0 {
			return err
		}
		return snaps[len(snaps)-1].Decode(v)
	}
	if err := latest(model.SnapWebhooks, &in.Webhooks); err != nil {
		return in, err
	}
	if err := latest(model.SnapPodSpecs, &in.Specs); err != nil {
		return in, err
	}
	if sensorsKnown {
		if err := latest(model.SnapFalconAPI, &in.Sensors); err != nil {
			return in, err
		}
	}
	return in, nil
}
