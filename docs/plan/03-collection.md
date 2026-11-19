# Part 3: Collection (Tasks 9–11)

All commands run from ``.

The collector never holds informer caches. Each tick it lists only what it needs and keeps a few small maps (previous container restart counts, node allocatable, node conditions, and recent eviction UIDs).

---

### Task 9: K8s and metrics adapters

**Files:**
- Create: `internal/collect/sources.go`
- Test: `internal/collect/sources_test.go`

- [ ] **Step 1: Write the failing test**

```go
package collect

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"
)

func TestQuantities(t *testing.T) {
	cpu := resource.MustParse("250m")
	mem := resource.MustParse("512Mi")
	if MilliCPU(cpu) != 250 || Bytes(mem) != 512<<20 {
		t.Fatalf("cpu=%d mem=%d", MilliCPU(cpu), Bytes(mem))
	}
	rl := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("8Gi")}
	c, m := CPUMem(rl)
	if c != 2000 || m != 8<<30 {
		t.Fatalf("resource list: %d %d", c, m)
	}
	if c, m := CPUMem(nil); c != 0 || m != 0 {
		t.Fatal("nil list")
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/collect/`
Expected: FAIL (`undefined: MilliCPU`)

- [ ] **Step 3: Implement**

```go
// Package collect samples Falcon usage and cluster state on two schedules:
// a fast tick (metrics + Falcon pods) and a slow tick (nodes, DaemonSets,
// webhooks, evictions, discovery, retention).
package collect

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	admissionv1client "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	authzv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

// Kube bundles the typed clients the tool uses. Only these groups are
// imported so the binary stays small.
type Kube struct {
	Core      corev1client.CoreV1Interface
	Apps      appsv1client.AppsV1Interface
	Admission admissionv1client.AdmissionregistrationV1Interface
	Authz     authzv1client.AuthorizationV1Interface
	Metrics   MetricsSource
}

type ContainerUsage struct {
	Name     string
	CPUm     int64
	MemBytes int64
}

type PodUsage struct {
	Namespace, Name string
	Containers      []ContainerUsage
}

type NodeUsage struct {
	Name     string
	CPUm     int64
	MemBytes int64
}

// MetricsSource is metrics.k8s.io. Faked in tests.
type MetricsSource interface {
	PodMetrics(ctx context.Context, namespace string) ([]PodUsage, error)
	NodeMetrics(ctx context.Context) ([]NodeUsage, error)
}

func NewKube(cfg *rest.Config) (Kube, error) {
	// Built-in types speak protobuf: smaller responses, cheaper decoding.
	// metrics.k8s.io stays JSON (aggregated API; protobuf support varies).
	pb := rest.CopyConfig(cfg)
	pb.ContentType = "application/vnd.kubernetes.protobuf"
	pb.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	core, err := corev1client.NewForConfig(pb)
	if err != nil {
		return Kube{}, err
	}
	apps, err := appsv1client.NewForConfig(pb)
	if err != nil {
		return Kube{}, err
	}
	adm, err := admissionv1client.NewForConfig(pb)
	if err != nil {
		return Kube{}, err
	}
	authz, err := authzv1client.NewForConfig(cfg)
	if err != nil {
		return Kube{}, err
	}
	m, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return Kube{}, err
	}
	return Kube{Core: core, Apps: apps, Admission: adm, Authz: authz, Metrics: metricsAPI{m}}, nil
}

type metricsAPI struct{ c metricsclient.MetricsV1beta1Interface }

func (m metricsAPI) PodMetrics(ctx context.Context, ns string) ([]PodUsage, error) {
	l, err := m.c.PodMetricses(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]PodUsage, 0, len(l.Items))
	for _, p := range l.Items {
		pu := PodUsage{Namespace: p.Namespace, Name: p.Name}
		for _, c := range p.Containers {
			cpu, mem := CPUMem(c.Usage)
			pu.Containers = append(pu.Containers, ContainerUsage{Name: c.Name, CPUm: cpu, MemBytes: mem})
		}
		out = append(out, pu)
	}
	return out, nil
}

func (m metricsAPI) NodeMetrics(ctx context.Context) ([]NodeUsage, error) {
	l, err := m.c.NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NodeUsage, 0, len(l.Items))
	for _, n := range l.Items {
		cpu, mem := CPUMem(n.Usage)
		out = append(out, NodeUsage{Name: n.Name, CPUm: cpu, MemBytes: mem})
	}
	return out, nil
}

func MilliCPU(q resource.Quantity) int64 { return q.MilliValue() }
func Bytes(q resource.Quantity) int64    { return q.Value() }

func CPUMem(rl corev1.ResourceList) (int64, int64) {
	var cpu, mem int64
	if q, ok := rl[corev1.ResourceCPU]; ok {
		cpu = MilliCPU(q)
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		mem = Bytes(q)
	}
	return cpu, mem
}
```

- [ ] **Step 4: Run the test**

Run: `gofmt -w internal/collect && go test ./internal/collect/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/collect go.mod go.sum
git commit -m "falcon-resource-test: add k8s and metrics adapters

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: Collector and fast tick

The fast tick does the following:
1. Lists pods in each Falcon namespace and classifies each one with `discover.ComponentForPod`.
2. Compares restart counts with the previous tick and records `restart`, `oomkill`, `crashloop`, and `ready_change` events. A pod's first sighting sets the baseline and emits nothing.
3. Writes a `SnapPodSpecs` snapshot, deduped per (component, container name).
4. Reads pod metrics for the Falcon namespaces, plus the tool's own pod, and node metrics. All rows get the **same TS**.

If a metrics call fails, it writes no samples for that tick (a gap) and records the error in `Status`.

**Files:**
- Create: `internal/collect/collector.go`
- Test: `internal/collect/collector_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package collect

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

var t0 = time.Unix(1_700_000_100, 0)

type fakeMetrics struct {
	pods  map[string][]PodUsage
	nodes []NodeUsage
	err   error
}

func (f *fakeMetrics) PodMetrics(_ context.Context, ns string) ([]PodUsage, error) {
	return f.pods[ns], f.err
}
func (f *fakeMetrics) NodeMetrics(context.Context) ([]NodeUsage, error) { return f.nodes, f.err }

func sensorPod(name, node string, restarts int32, lastReason string) *corev1.Pod {
	st := corev1.ContainerStatus{Name: "falcon-node-sensor", RestartCount: restarts, Ready: true}
	if lastReason != "" {
		st.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: lastReason}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-system", Name: name,
			OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "falcon-sensor"}}},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name: "falcon-node-sensor", Image: "r/falcon-sensor:7.20.0-17306",
			Env: []corev1.EnvVar{{Name: "FALCONCTL_OPT_BACKEND", Value: "bpf"}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("500Mi")}},
		}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{st}},
	}
}

func newTestCollector(t *testing.T, m *fakeMetrics, objs ...any) (*Collector, *store.Store, *fake.Clientset) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "fp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := fake.NewSimpleClientset()
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{})
		}
	}
	kube := Kube{Core: cs.CoreV1(), Apps: cs.AppsV1(), Admission: cs.AdmissionregistrationV1(), Authz: cs.AuthorizationV1(), Metrics: m}
	c := New(kube, st, Config{Interval: 15 * time.Second, SelfNamespace: "falcon-resource-test", SelfPod: "fp-0", RawRetention: 24 * time.Hour, Retention: 24 * time.Hour})
	now := t0
	c.now = func() time.Time { return now }
	c.setDiscovery(discover.Result{
		Workloads:  []discover.Workload{{Component: model.DaemonSetSensor, Namespace: "falcon-system", Kind: "DaemonSet", Name: "falcon-sensor"}},
		Namespaces: []string{"falcon-system"},
	}, t0)
	c.alloc = map[string][2]int64{"n1": {2000, 8 << 30}}
	return c, st, cs
}

func TestFastTickWritesSamples(t *testing.T) {
	m := &fakeMetrics{
		pods: map[string][]PodUsage{
			"falcon-system":    {{Namespace: "falcon-system", Name: "s1", Containers: []ContainerUsage{{Name: "falcon-node-sensor", CPUm: 40, MemBytes: 300 << 20}}}, {Namespace: "falcon-system", Name: "helm-test", Containers: []ContainerUsage{{Name: "t", CPUm: 1}}}},
			"falcon-resource-test": {{Namespace: "falcon-resource-test", Name: "fp-0", Containers: []ContainerUsage{{Name: "falcon-resource-test", CPUm: 3, MemBytes: 30 << 20}}}, {Namespace: "falcon-resource-test", Name: "other", Containers: []ContainerUsage{{Name: "x", CPUm: 1}}}},
		},
		nodes: []NodeUsage{{Name: "n1", CPUm: 500, MemBytes: 2 << 30}},
	}
	c, st, _ := newTestCollector(t, m, sensorPod("s1", "n1", 0, ""))
	if err := c.FastTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	ss, _ := st.Samples(t0, t0.Add(time.Second))
	if len(ss) != 2 {
		t.Fatalf("want sensor + self only (non-Falcon pods skipped), got %+v", ss)
	}
	for _, s := range ss {
		if s.Pod == "s1" && (s.Component != model.DaemonSetSensor || s.Node != "n1") {
			t.Fatalf("sensor sample: %+v", s)
		}
		if s.Pod == "fp-0" && s.Component != model.Self {
			t.Fatalf("self sample: %+v", s)
		}
	}
	ns, _ := st.NodeSamples(t0, t0.Add(time.Second))
	if len(ns) != 1 || ns[0].AllocCPUm != 2000 || ns[0].UsedCPUm != 500 {
		t.Fatalf("node samples: %+v", ns)
	}
	snap, ok, _ := st.LatestSnapshot(model.SnapPodSpecs)
	var specs []model.ContainerSpec
	snap.Decode(&specs)
	if !ok || len(specs) != 1 || specs[0].Version != "7.20.0-17306" || specs[0].Backend != "bpf" || specs[0].ReqCPUm != 250 {
		t.Fatalf("pod specs: %+v", specs)
	}
	if s := c.Status(); !s.MetricsOK || len(s.Pods) != 1 || len(s.Latest) != 2 {
		t.Fatalf("status: %+v", s)
	}
}

func TestFastTickRestartEvents(t *testing.T) {
	m := &fakeMetrics{}
	c, st, cs := newTestCollector(t, m, sensorPod("s1", "n1", 0, ""))
	ctx := context.Background()
	c.FastTick(ctx) // baseline, no events
	cs.CoreV1().Pods("falcon-system").Update(ctx, sensorPod("s1", "n1", 1, "OOMKilled"), metav1.UpdateOptions{})
	c.now = func() time.Time { return t0.Add(15 * time.Second) }
	c.FastTick(ctx)
	evs, _ := st.Events(t0, t0.Add(time.Minute))
	kinds := map[model.EventKind]int{}
	for _, e := range evs {
		if !e.Falcon {
			t.Fatalf("event should be marked falcon: %+v", e)
		}
		kinds[e.Kind]++
	}
	if kinds[model.EventRestart] != 1 || kinds[model.EventOOMKill] != 1 {
		t.Fatalf("want 1 restart + 1 oomkill, got %+v", kinds)
	}
}

func TestFastTickMetricsDown(t *testing.T) {
	m := &fakeMetrics{err: errors.New("the server could not find the requested resource")}
	c, st, _ := newTestCollector(t, m, sensorPod("s1", "n1", 0, ""))
	c.FastTick(context.Background())
	ss, _ := st.Samples(t0, t0.Add(time.Second))
	if len(ss) != 0 {
		t.Fatal("no samples when metrics are down")
	}
	if s := c.Status(); s.MetricsOK || s.MetricsErr == "" || len(s.Pods) != 1 {
		t.Fatalf("status should record the error and still list pods: %+v", s)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/collect/`
Expected: FAIL (`undefined: New`)

- [ ] **Step 3: Implement**

```go
package collect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

type Config struct {
	Interval       time.Duration
	SelfNamespace  string
	SelfPod        string
	Namespaces     []string      // discovery override; empty = all
	RawRetention   time.Duration // raw 15s samples kept this long, then rolled up
	Retention      time.Duration // everything older is deleted
	CallTimeout    time.Duration // per API call; default 5s
}

// PodState is the live view of one Falcon pod.
type PodState struct {
	Component model.Component `json:"component"`
	Namespace string          `json:"namespace"`
	Name      string          `json:"name"`
	Node      string          `json:"node"`
	Ready     bool            `json:"ready"`
	Restarts  int32           `json:"restarts"`
	Phase     string          `json:"phase"`
}

type Status struct {
	LastTick     time.Time          `json:"last_tick"`
	MetricsOK    bool               `json:"metrics_ok"`
	MetricsErr   string             `json:"metrics_error,omitempty"`
	Discovery    discover.Result    `json:"discovery"`
	DiscoveredAt time.Time          `json:"discovered_at"`
	DiscoveryErr string             `json:"discovery_error,omitempty"`
	KACPresent   bool               `json:"kac_present"`
	Nodes        int                `json:"nodes"`
	Pods         []PodState         `json:"pods"`
	Latest       []model.Sample     `json:"-"`
	LatestNodes  []model.NodeSample `json:"-"`
}

type Collector struct {
	kube Kube
	st   *store.Store
	cfg  Config
	now  func() time.Time

	mu     sync.RWMutex
	status Status

	// owned by the tick goroutines (fast and slow run on one loop in main)
	prevRestarts map[string]int32 // ns/pod/container -> restartCount
	prevWaiting  map[string]string
	prevReady    map[string]bool
	alloc        map[string][2]int64 // node -> allocatable cpu_m, mem
	pressure     map[string]bool     // node/condition -> true
	seenEvict    map[string]time.Time
	lastRetention time.Time
}

func New(k Kube, st *store.Store, cfg Config) *Collector {
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = 5 * time.Second
	}
	return &Collector{
		kube: k, st: st, cfg: cfg, now: time.Now,
		prevRestarts: map[string]int32{}, prevWaiting: map[string]string{}, prevReady: map[string]bool{},
		alloc: map[string][2]int64{}, pressure: map[string]bool{}, seenEvict: map[string]time.Time{},
	}
}

func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Collector) setDiscovery(r discover.Result, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Discovery, c.status.DiscoveredAt, c.status.DiscoveryErr = r, at, ""
	c.status.KACPresent = false
	for _, w := range r.Workloads {
		if w.Component == model.KAC {
			c.status.KACPresent = true
		}
	}
}

func (c *Collector) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.cfg.CallTimeout)
}

// FastTick samples Falcon pods and metrics. Called every cfg.Interval.
func (c *Collector) FastTick(ctx context.Context) error {
	now := c.now().Truncate(time.Second)
	disc := c.Status().Discovery

	// 1. Falcon pods: classify, detect restarts, collect specs.
	podComp := map[string]PodState{} // ns/pod -> state
	var pods []PodState
	specs := map[string]model.ContainerSpec{}
	for _, ns := range disc.Namespaces {
		cctx, cancel := c.call(ctx)
		l, err := c.kube.Core.Pods(ns).List(cctx, metav1.ListOptions{})
		cancel()
		if err != nil {
			continue // RBAC/timeout: pods panel shows what we have; /setup explains
		}
		for i := range l.Items {
			p := &l.Items[i]
			comp, ok := discover.ComponentForPod(p)
			if !ok {
				continue
			}
			ps := c.observePod(now, comp, p)
			podComp[p.Namespace+"/"+p.Name] = ps
			pods = append(pods, ps)
			for _, ct := range p.Spec.Containers {
				k := string(comp) + "/" + ct.Name
				if _, dup := specs[k]; !dup {
					specs[k] = containerSpec(comp, p, ct)
				}
			}
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Namespace+pods[i].Name < pods[j].Namespace+pods[j].Name })
	if len(specs) > 0 {
		list := make([]model.ContainerSpec, 0, len(specs))
		for _, s := range specs {
			list = append(list, s)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Component+model.Component(list[i].Container) < list[j].Component+model.Component(list[j].Container) })
		if err := c.st.PutSnapshot(now, model.SnapPodSpecs, list); err != nil {
			return err
		}
	}

	// 2. Metrics. Any failure = no samples this tick (a gap), never partial.
	samples, nodes, merr := c.readMetrics(ctx, now, disc.Namespaces, podComp)
	if merr == nil {
		if err := c.st.InsertSamples(samples); err != nil {
			return err
		}
		if err := c.st.InsertNodeSamples(nodes); err != nil {
			return err
		}
	}

	c.mu.Lock()
	c.status.LastTick, c.status.Pods = now, pods
	c.status.MetricsOK, c.status.MetricsErr = merr == nil, ""
	if merr != nil {
		c.status.MetricsErr = merr.Error()
	} else {
		c.status.Latest, c.status.LatestNodes = samples, nodes
	}
	c.mu.Unlock()
	return nil
}

func (c *Collector) readMetrics(ctx context.Context, now time.Time, namespaces []string, podComp map[string]PodState) ([]model.Sample, []model.NodeSample, error) {
	var samples []model.Sample
	for _, ns := range append(append([]string{}, namespaces...), c.cfg.SelfNamespace) {
		cctx, cancel := c.call(ctx)
		pm, err := c.kube.Metrics.PodMetrics(cctx, ns)
		cancel()
		if err != nil {
			return nil, nil, fmt.Errorf("pod metrics (%s): %w", ns, err)
		}
		for _, p := range pm {
			comp, node := model.Component(""), ""
			if ns == c.cfg.SelfNamespace && p.Name == c.cfg.SelfPod {
				comp = model.Self
			} else if ps, ok := podComp[ns+"/"+p.Name]; ok {
				comp, node = ps.Component, ps.Node
			} else {
				continue
			}
			for _, ct := range p.Containers {
				samples = append(samples, model.Sample{TS: now, Node: node, Namespace: ns, Pod: p.Name, Container: ct.Name,
					Component: comp, CPUm: ct.CPUm, MemBytes: ct.MemBytes, CPUMaxm: ct.CPUm, MemMax: ct.MemBytes})
			}
		}
	}
	cctx, cancel := c.call(ctx)
	nm, err := c.kube.Metrics.NodeMetrics(cctx)
	cancel()
	if err != nil {
		return nil, nil, fmt.Errorf("node metrics: %w", err)
	}
	if len(c.alloc) == 0 {
		c.refreshNodes(ctx, now, false)
	}
	nodes := make([]model.NodeSample, 0, len(nm))
	for _, n := range nm {
		a, ok := c.alloc[n.Name]
		if !ok {
			continue // node joined since last slow tick; picked up next minute
		}
		nodes = append(nodes, model.NodeSample{TS: now, Node: n.Name, AllocCPUm: a[0], AllocMem: a[1], UsedCPUm: n.CPUm, UsedMem: n.MemBytes})
	}
	return samples, nodes, nil
}

// observePod updates restart/ready baselines and emits events on change.
func (c *Collector) observePod(now time.Time, comp model.Component, p *corev1.Pod) PodState {
	ps := PodState{Component: comp, Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName, Phase: string(p.Status.Phase), Ready: len(p.Status.ContainerStatuses) > 0}
	emit := func(kind model.EventKind, reason, msg string) {
		c.st.InsertEvent(model.Event{TS: now, Kind: kind, Namespace: p.Namespace, Object: p.Name, Node: p.Spec.NodeName, Reason: reason, Message: msg, Falcon: true})
	}
	for _, cs := range p.Status.ContainerStatuses {
		key := p.Namespace + "/" + p.Name + "/" + cs.Name
		ps.Restarts += cs.RestartCount
		ps.Ready = ps.Ready && cs.Ready
		prev, seen := c.prevRestarts[key]
		if seen && cs.RestartCount > prev {
			reason := ""
			if t := cs.LastTerminationState.Terminated; t != nil {
				reason = t.Reason
			}
			emit(model.EventRestart, reason, fmt.Sprintf("container %s restarted (%d -> %d)", cs.Name, prev, cs.RestartCount))
			if reason == "OOMKilled" {
				emit(model.EventOOMKill, reason, fmt.Sprintf("container %s was OOMKilled", cs.Name))
			}
		}
		waiting := ""
		if w := cs.State.Waiting; w != nil {
			waiting = w.Reason
		}
		if seen && waiting == "CrashLoopBackOff" && c.prevWaiting[key] != waiting {
			emit(model.EventCrashLoop, waiting, fmt.Sprintf("container %s is in CrashLoopBackOff", cs.Name))
		}
		if pr, ok := c.prevReady[key]; ok && pr != cs.Ready {
			emit(model.EventReadyChange, fmt.Sprintf("ready=%v", cs.Ready), cs.Name)
		}
		c.prevRestarts[key], c.prevWaiting[key], c.prevReady[key] = cs.RestartCount, waiting, cs.Ready
	}
	return ps
}

func containerSpec(comp model.Component, p *corev1.Pod, ct corev1.Container) model.ContainerSpec {
	s := model.ContainerSpec{Component: comp, Namespace: p.Namespace, Pod: p.Name, Container: ct.Name, Node: p.Spec.NodeName,
		Image: ct.Image, Version: imageTag(ct.Image), PriorityClass: p.Spec.PriorityClassName}
	s.ReqCPUm, s.ReqMem = CPUMem(ct.Resources.Requests)
	s.LimCPUm, s.LimMem = CPUMem(ct.Resources.Limits)
	if comp == model.DaemonSetSensor {
		s.Backend = "kernel"
		for _, e := range ct.Env {
			if e.Name == "FALCONCTL_OPT_BACKEND" && e.Value != "" {
				s.Backend = e.Value
			}
		}
	}
	return s
}

func imageTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return ""
}
```

`refreshNodes` is defined in Task 11. Until Task 11 is done, add a temporary stub at the end of `collector.go` so this task compiles and its tests pass:

```go
func (c *Collector) refreshNodes(ctx context.Context, now time.Time, emit bool) {}
```

Task 11, Step 3 removes this stub.

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/collect && go test ./internal/collect/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/collect
git commit -m "falcon-resource-test: add collector fast tick

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 11: Slow tick (discovery, nodes, DaemonSets, webhooks, evictions, retention)

Runs every 60s. Each step works independently, so a failure in one (for example, missing webhook RBAC) doesn't stop the others.

- **Discovery** reruns once at least 5 minutes have passed since the last run.
- **Nodes:** keeps the allocatable cache current and emits a `pressure` event when a Memory/Disk/PID pressure condition turns True.
- **DaemonSets:** writes `SnapDaemonSets` for DaemonSet Sensor workloads only.
- **Webhooks:** writes `SnapWebhooks` for Falcon webhooks only. A webhook counts as Falcon when its name contains `falcon`, or when its client service is in a Falcon namespace.
- **Evictions:** reads cluster-wide `Event`s with `fieldSelector=reason=Evicted`, deduped by UID. An eviction is marked `Falcon: true` when the pod is in a Falcon namespace **and** its name starts with a discovered workload name.
- **Retention** runs every 10 minutes.

**Files:**
- Create: `internal/collect/slow.go`
- Modify: `internal/collect/collector.go` (remove the `refreshNodes` stub)
- Test: `internal/collect/slow_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package collect

import (
	"context"
	"testing"
	"time"

	admv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

func node(name string, pressure bool) *corev1.Node {
	st := corev1.ConditionFalse
	if pressure {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
		Conditions:  []corev1.NodeCondition{{Type: corev1.NodeMemoryPressure, Status: st}},
	}}
}

func TestSlowTick(t *testing.T) {
	c, st, cs := newTestCollector(t, &fakeMetrics{})
	ctx := context.Background()
	ignore, fail := admv1.Ignore, admv1.Fail
	timeout := int32(10)
	cs.CoreV1().Nodes().Create(ctx, node("n1", false), metav1.CreateOptions{})
	cs.AppsV1().DaemonSets("falcon-system").Create(ctx, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-system", Name: "falcon-sensor"},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "r/falcon-sensor:7"}}}}},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, NumberReady: 2, UpdatedNumberScheduled: 3},
	}, metav1.CreateOptions{})
	cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, &admv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "validating.falcon-kac.crowdstrike.com"},
		Webhooks:   []admv1.ValidatingWebhook{{Name: "validating.falcon-kac.crowdstrike.com", FailurePolicy: &ignore, TimeoutSeconds: &timeout}},
	}, metav1.CreateOptions{})
	cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, &admv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "gatekeeper"},
		Webhooks:   []admv1.ValidatingWebhook{{Name: "gatekeeper", FailurePolicy: &fail}},
	}, metav1.CreateOptions{})
	cs.CoreV1().Events("falcon-system").Create(ctx, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "falcon-system", Name: "e1", UID: "u1"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "falcon-system", Name: "falcon-sensor-abcde"},
		Reason:         "Evicted", Message: "The node was low on resource: memory.",
		Source:         corev1.EventSource{Host: "n1"},
		LastTimestamp:  metav1.NewTime(t0),
	}, metav1.CreateOptions{})

	c.SlowTick(ctx)
	c.SlowTick(ctx) // eviction deduped by UID

	var ds []model.DaemonSetState
	snap, _, _ := st.LatestSnapshot(model.SnapDaemonSets)
	snap.Decode(&ds)
	if len(ds) != 1 || ds[0].Ready != 2 || ds[0].Desired != 3 {
		t.Fatalf("daemonsets: %+v", ds)
	}
	var hooks []model.WebhookState
	snap, _, _ = st.LatestSnapshot(model.SnapWebhooks)
	snap.Decode(&hooks)
	if len(hooks) != 1 || hooks[0].FailurePolicy != "Ignore" || hooks[0].TimeoutSeconds != 10 {
		t.Fatalf("only falcon webhooks: %+v", hooks)
	}
	evs, _ := st.Events(t0.Add(-time.Hour), t0.Add(time.Hour))
	if len(evs) != 1 || evs[0].Kind != model.EventEviction || !evs[0].Falcon || evs[0].Node != "n1" {
		t.Fatalf("eviction: %+v", evs)
	}
	if c.alloc["n1"][0] != 4000 || c.Status().Nodes != 1 {
		t.Fatalf("alloc: %+v", c.alloc)
	}

	// pressure transition emits once
	cs.CoreV1().Nodes().Update(ctx, node("n1", true), metav1.UpdateOptions{})
	c.SlowTick(ctx)
	c.SlowTick(ctx)
	evs, _ = st.Events(t0.Add(-time.Hour), t0.Add(time.Hour))
	n := 0
	for _, e := range evs {
		if e.Kind == model.EventPressure && e.Reason == "MemoryPressure" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one pressure event, got %d", n)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/collect/ -run Slow`
Expected: FAIL (`c.SlowTick undefined`)

- [ ] **Step 3: Remove the stub from `collector.go`**

Delete this line from `internal/collect/collector.go`:

```go
func (c *Collector) refreshNodes(ctx context.Context, now time.Time, emit bool) {}
```

- [ ] **Step 4: Implement `slow.go`**

```go
package collect

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

const (
	discoveryEvery = 5 * time.Minute
	retentionEvery = 10 * time.Minute
)

// SlowTick refreshes cluster state. Called every 60s. Each step is
// independent; errors are swallowed here and surfaced via /setup.
func (c *Collector) SlowTick(ctx context.Context) {
	now := c.now().Truncate(time.Second)
	if st := c.Status(); st.DiscoveredAt.IsZero() || now.Sub(st.DiscoveredAt) >= discoveryEvery {
		c.Discover(ctx)
	}
	c.refreshNodes(ctx, now, true)
	c.refreshDaemonSets(ctx, now)
	c.refreshWebhooks(ctx, now)
	c.refreshEvictions(ctx, now)
	if now.Sub(c.lastRetention) >= retentionEvery {
		c.st.Rollup(now.Add(-c.cfg.RawRetention))
		c.st.Expire(now.Add(-c.cfg.Retention))
		c.lastRetention = now
	}
}

func (c *Collector) Discover(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := discover.Discover(cctx, c.kube.Apps, c.cfg.Namespaces)
	if err != nil {
		c.mu.Lock()
		c.status.DiscoveryErr = err.Error()
		c.mu.Unlock()
		return
	}
	c.setDiscovery(res, c.now())
}

var pressureTypes = []corev1.NodeConditionType{corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure}

func (c *Collector) refreshNodes(ctx context.Context, now time.Time, emit bool) {
	cctx, cancel := c.call(ctx)
	defer cancel()
	l, err := c.kube.Core.Nodes().List(cctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	alloc := make(map[string][2]int64, len(l.Items))
	for _, n := range l.Items {
		cpu, mem := CPUMem(n.Status.Allocatable)
		alloc[n.Name] = [2]int64{cpu, mem}
		for _, cond := range n.Status.Conditions {
			for _, pt := range pressureTypes {
				if cond.Type != pt {
					continue
				}
				key := n.Name + "/" + string(pt)
				on := cond.Status == corev1.ConditionTrue
				if on && !c.pressure[key] && emit {
					c.st.InsertEvent(model.Event{TS: now, Kind: model.EventPressure, Node: n.Name, Object: n.Name, Reason: string(pt), Message: cond.Message})
				}
				c.pressure[key] = on
			}
		}
	}
	c.alloc = alloc
	c.mu.Lock()
	c.status.Nodes = len(alloc)
	c.mu.Unlock()
}

func (c *Collector) refreshDaemonSets(ctx context.Context, now time.Time) {
	var out []model.DaemonSetState
	for _, w := range c.Status().Discovery.Workloads {
		if w.Component != model.DaemonSetSensor {
			continue
		}
		cctx, cancel := c.call(ctx)
		d, err := c.kube.Apps.DaemonSets(w.Namespace).Get(cctx, w.Name, metav1.GetOptions{})
		cancel()
		if err != nil {
			continue
		}
		out = append(out, model.DaemonSetState{Namespace: d.Namespace, Name: d.Name,
			Desired: d.Status.DesiredNumberScheduled, Ready: d.Status.NumberReady, Updated: d.Status.UpdatedNumberScheduled})
	}
	if len(out) > 0 {
		c.st.PutSnapshot(now, model.SnapDaemonSets, out)
	}
}

func (c *Collector) isFalconHook(name string, svcNamespace string) bool {
	if strings.Contains(strings.ToLower(name), "falcon") {
		return true
	}
	for _, ns := range c.Status().Discovery.Namespaces {
		if ns == svcNamespace {
			return true
		}
	}
	return false
}

func (c *Collector) refreshWebhooks(ctx context.Context, now time.Time) {
	var out []model.WebhookState
	str := func(p *string) string {
		if p == nil {
			return "Fail" // API default for admissionregistration/v1
		}
		return *p
	}
	sec := func(p *int32) int32 {
		if p == nil {
			return 10 // API default
		}
		return *p
	}
	cctx, cancel := c.call(ctx)
	v, err := c.kube.Admission.ValidatingWebhookConfigurations().List(cctx, metav1.ListOptions{})
	cancel()
	if err == nil {
		for _, cfg := range v.Items {
			for _, h := range cfg.Webhooks {
				ns := ""
				if h.ClientConfig.Service != nil {
					ns = h.ClientConfig.Service.Namespace
				}
				if c.isFalconHook(cfg.Name+" "+h.Name, ns) {
					fp := (*string)(h.FailurePolicy)
					out = append(out, model.WebhookState{Name: h.Name, Kind: "validating", FailurePolicy: str(fp), TimeoutSeconds: sec(h.TimeoutSeconds)})
				}
			}
		}
	}
	cctx, cancel = c.call(ctx)
	m, err := c.kube.Admission.MutatingWebhookConfigurations().List(cctx, metav1.ListOptions{})
	cancel()
	if err == nil {
		for _, cfg := range m.Items {
			for _, h := range cfg.Webhooks {
				ns := ""
				if h.ClientConfig.Service != nil {
					ns = h.ClientConfig.Service.Namespace
				}
				if c.isFalconHook(cfg.Name+" "+h.Name, ns) {
					fp := (*string)(h.FailurePolicy)
					out = append(out, model.WebhookState{Name: h.Name, Kind: "mutating", FailurePolicy: str(fp), TimeoutSeconds: sec(h.TimeoutSeconds)})
				}
			}
		}
	}
	c.st.PutSnapshot(now, model.SnapWebhooks, out)
}

func (c *Collector) refreshEvictions(ctx context.Context, now time.Time) {
	cctx, cancel := c.call(ctx)
	l, err := c.kube.Core.Events(metav1.NamespaceAll).List(cctx, metav1.ListOptions{FieldSelector: "reason=Evicted"})
	cancel()
	if err != nil {
		return
	}
	disc := c.Status().Discovery
	for _, e := range l.Items {
		uid := string(e.UID)
		if _, seen := c.seenEvict[uid]; seen {
			continue
		}
		c.seenEvict[uid] = now
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.EventTime.Time
		}
		if ts.IsZero() {
			ts = now
		}
		node := e.Source.Host
		if node == "" {
			node = e.ReportingInstance
		}
		obj := e.InvolvedObject
		falcon := false
		for _, w := range disc.Workloads {
			if w.Namespace == obj.Namespace && strings.HasPrefix(obj.Name, w.Name) {
				falcon = true
			}
		}
		c.st.InsertEvent(model.Event{TS: ts, Kind: model.EventEviction, Namespace: obj.Namespace, Object: obj.Name,
			Node: node, Reason: e.Reason, Message: e.Message, Falcon: falcon})
	}
	for uid, at := range c.seenEvict {
		if now.Sub(at) > 2*time.Hour {
			delete(c.seenEvict, uid)
		}
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -w internal/collect && go vet ./internal/collect/ && go test ./internal/collect/`
Expected: `ok`. The fake clientset ignores field selectors, so the test only creates events whose reason is `Evicted`.

- [ ] **Step 6: Commit**

```bash
git add internal/collect
git commit -m "falcon-resource-test: add collector slow tick

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
