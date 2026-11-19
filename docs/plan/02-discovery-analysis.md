# Part 2: Discovery and Analysis (Tasks 5–8)

All commands run from ``. Module prefix below is abbreviated in prose as `.../internal/...`; code uses the full path `github.com/vianneyp72/falcon-resource-test/internal/...`.

---

### Task 5: Discovery

Finds Falcon workloads by image name or `app.kubernetes.io/name` label. Works for Helm and Operator installs. The override `discovery.namespaces` limits the search. (The spec's `extraSelectors` is dropped for v1: image and label matching already cover the Helm, Operator, and registry-mirrored installs.)

**Files:**
- Create: `internal/discover/discover.go`
- Test: `internal/discover/discover_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package discover

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

func podSpec(images ...string) corev1.PodTemplateSpec {
	var cs []corev1.Container
	for _, im := range images {
		cs = append(cs, corev1.Container{Name: "c", Image: im})
	}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: cs}}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		kind, image string
		labels      map[string]string
		want        model.Component
		ok          bool
	}{
		{"DaemonSet", "registry.crowdstrike.com/falcon-sensor/us-1/release/falcon-sensor:7.20.0-17306-1.falcon-linux.Release.US-1", nil, model.DaemonSetSensor, true},
		{"DaemonSet", "123.dkr.ecr.us-east-1.amazonaws.com/falcon-sensor@sha256:abc", nil, model.DaemonSetSensor, true},
		{"Deployment", "registry.crowdstrike.com/falcon-kac/us-1/release/falcon-kac:7.39.0-3702", nil, model.KAC, true},
		{"Deployment", "myacr.azurecr.io/falcon-imageanalyzer:1.0.25", nil, model.IAR, true},
		{"Deployment", "myregistry/falcon-container:7.39.0-7802", nil, model.Injector, true},
		{"Deployment", "quay.io/crowdstrike/falcon-operator:1.3.0", nil, model.Operator, true},
		{"Deployment", "mirror/custom-name:1", map[string]string{"app.kubernetes.io/name": "falcon-kac"}, model.KAC, true},
		{"Deployment", "nginx:1.27", nil, "", false},
		// falcon-container inside a DaemonSet is not the injector
		{"DaemonSet", "myregistry/falcon-container:7", nil, "", false},
	}
	for _, c := range cases {
		got, ok := Classify(c.kind, []string{c.image}, c.labels)
		if got != c.want || ok != c.ok {
			t.Errorf("Classify(%s, %s) = %q,%v want %q,%v", c.kind, c.image, got, ok, c.want, c.ok)
		}
	}
}

func TestImageName(t *testing.T) {
	for in, want := range map[string]string{
		"falcon-sensor":                      "falcon-sensor",
		"a/b/falcon-sensor:1.2":              "falcon-sensor",
		"host:5000/falcon-kac@sha256:deadbe": "falcon-kac",
		"host:5000/falcon-kac:7.1@sha256:de": "falcon-kac",
	} {
		if got := ImageName(in); got != want {
			t.Errorf("ImageName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-system", Name: "falcon-sensor"},
			Spec: appsv1.DaemonSetSpec{Template: podSpec("r/falcon-sensor:7")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "falcon-kac", Name: "falcon-kac"},
			Spec: appsv1.DeploymentSpec{Template: podSpec("r/falcon-kac:7", "r/falcon-kac:7")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
			Spec: appsv1.DeploymentSpec{Template: podSpec("nginx")}},
	)
	res, err := Discover(context.Background(), cs.AppsV1(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Workloads) != 2 {
		t.Fatalf("want 2 workloads, got %+v", res.Workloads)
	}
	if len(res.Namespaces) != 2 || res.Namespaces[0] != "falcon-kac" || res.Namespaces[1] != "falcon-system" {
		t.Fatalf("namespaces sorted+deduped: %v", res.Namespaces)
	}
	if res.Workloads[0].Component != model.KAC {
		t.Fatalf("workloads sorted by namespace: %+v", res.Workloads)
	}

	// override limits the search
	res, _ = Discover(context.Background(), cs.AppsV1(), []string{"falcon-system"})
	if len(res.Workloads) != 1 || res.Workloads[0].Kind != "DaemonSet" {
		t.Fatalf("override: %+v", res.Workloads)
	}
}

func TestComponentForPod(t *testing.T) {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet"}}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "r/falcon-sensor:7"}}},
	}
	if c, ok := ComponentForPod(p); !ok || c != model.DaemonSetSensor {
		t.Fatalf("sensor pod: %q %v", c, ok)
	}
	p = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet"}}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "r/falcon-container:7"}}},
	}
	if c, ok := ComponentForPod(p); !ok || c != model.Injector {
		t.Fatalf("injector pod: %q %v", c, ok)
	}
	p = &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}}}
	if _, ok := ComponentForPod(p); ok {
		t.Fatal("helm test pod should not classify")
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go get k8s.io/client-go/kubernetes/fake && go test ./internal/discover/`
Expected: FAIL (`undefined: Classify`)

- [ ] **Step 3: Implement**

```go
// Package discover finds Falcon workloads in the cluster. It lists
// DaemonSets and Deployments (paged, nothing cached) and classifies them by
// image name or app.kubernetes.io/name label.
package discover

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

type Workload struct {
	Component model.Component `json:"component"`
	Namespace string          `json:"namespace"`
	Kind      string          `json:"kind"` // DaemonSet | Deployment
	Name      string          `json:"name"`
}

type Result struct {
	Workloads  []Workload `json:"workloads"`
	Namespaces []string   `json:"namespaces"`
}

var byName = map[string]model.Component{
	"falcon-sensor":        model.DaemonSetSensor,
	"falcon-kac":           model.KAC,
	"falcon-imageanalyzer": model.IAR,
	"falcon-container":     model.Injector,
	"falcon-operator":      model.Operator,
}

// ImageName returns the last path segment of an image ref, without tag or digest.
func ImageName(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	if i := strings.Index(ref, ":"); i >= 0 {
		ref = ref[:i]
	}
	return ref
}

// Classify decides which Falcon component a workload is. kind is the owning
// workload kind ("DaemonSet" or "Deployment"). The sensor only counts as a
// DaemonSet; falcon-container only counts as the injector Deployment.
func Classify(kind string, images []string, labels map[string]string) (model.Component, bool) {
	cand := []string{labels["app.kubernetes.io/name"]}
	for _, im := range images {
		cand = append(cand, ImageName(im))
	}
	for _, n := range cand {
		c, ok := byName[n]
		if !ok {
			continue
		}
		if c == model.DaemonSetSensor && kind != "DaemonSet" {
			continue
		}
		if c != model.DaemonSetSensor && kind == "DaemonSet" {
			continue
		}
		return c, true
	}
	return "", false
}

// ComponentForPod classifies a running pod using its owner kind
// (ReplicaSet counts as Deployment).
func ComponentForPod(p *corev1.Pod) (model.Component, bool) {
	kind := "Deployment"
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			kind = "DaemonSet"
		}
	}
	return Classify(kind, images(p.Spec), p.Labels)
}

func images(s corev1.PodSpec) []string {
	var out []string
	for _, c := range s.Containers {
		out = append(out, c.Image)
	}
	return out
}

// Discover lists workloads in the given namespaces (nil = all namespaces).
func Discover(ctx context.Context, apps appsv1client.AppsV1Interface, namespaces []string) (Result, error) {
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	var res Result
	for _, ns := range namespaces {
		opts := metav1.ListOptions{Limit: 200}
		for {
			ds, err := apps.DaemonSets(ns).List(ctx, opts)
			if err != nil {
				return res, err
			}
			for _, d := range ds.Items {
				if c, ok := Classify("DaemonSet", images(d.Spec.Template.Spec), d.Spec.Template.Labels); ok {
					res.Workloads = append(res.Workloads, Workload{c, d.Namespace, "DaemonSet", d.Name})
				}
			}
			if ds.Continue == "" {
				break
			}
			opts.Continue = ds.Continue
		}
		opts = metav1.ListOptions{Limit: 200}
		for {
			dep, err := apps.Deployments(ns).List(ctx, opts)
			if err != nil {
				return res, err
			}
			for _, d := range dep.Items {
				if c, ok := Classify("Deployment", images(d.Spec.Template.Spec), d.Spec.Template.Labels); ok {
					res.Workloads = append(res.Workloads, Workload{c, d.Namespace, "Deployment", d.Name})
				}
			}
			if dep.Continue == "" {
				break
			}
			opts.Continue = dep.Continue
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool {
		a, b := res.Workloads[i], res.Workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	seen := map[string]bool{}
	for _, w := range res.Workloads {
		if !seen[w.Namespace] {
			seen[w.Namespace] = true
			res.Namespaces = append(res.Namespaces, w.Namespace)
		}
	}
	return res, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/discover && go test ./internal/discover/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/discover go.mod go.sum
git commit -m "falcon-resource-test: add Falcon workload discovery

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: Stats helpers

**Files:**
- Create: `internal/analyze/stats.go`
- Test: `internal/analyze/stats_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package analyze

import "testing"

func TestPercentileNearestRank(t *testing.T) {
	v := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := map[float64]int64{0.5: 5, 0.95: 10, 0.9: 9, 0.1: 1, 1: 10}
	for p, want := range cases {
		if got := Percentile(v, p); got != want {
			t.Errorf("p%.0f = %d want %d", p*100, got, want)
		}
	}
	if Percentile(nil, 0.5) != 0 {
		t.Error("empty input should be 0")
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]int64{30, 10, 20}, []int64{30, 10, 50})
	if s.Avg != 20 || s.P50 != 20 || s.P95 != 30 || s.Peak != 50 {
		t.Fatalf("got %+v", s)
	}
	if (Summarize(nil, nil) != Stat{}) {
		t.Fatal("empty should be zero")
	}
}

func TestPct(t *testing.T) {
	if Pct(1, 4) != 25 || Pct(1, 0) != 0 {
		t.Fatal("pct")
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/analyze/`
Expected: FAIL (`undefined: Percentile`)

- [ ] **Step 3: Implement**

```go
package analyze

import "sort"

// Stat summarizes one metric over a window. CPU in millicores, memory in bytes.
type Stat struct {
	Avg  int64 `json:"avg"`
	P50  int64 `json:"p50"`
	P95  int64 `json:"p95"`
	Peak int64 `json:"peak"`
}

// Percentile uses nearest-rank on a copy of v (input order is not changed).
func Percentile(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(p*float64(len(s))+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}

// Summarize computes avg/p50/p95 from vals and Peak from maxes (rollup rows
// carry a bucket max that is higher than the bucket average).
func Summarize(vals, maxes []int64) Stat {
	if len(vals) == 0 {
		return Stat{}
	}
	var sum, peak int64
	for _, v := range vals {
		sum += v
	}
	for _, m := range maxes {
		if m > peak {
			peak = m
		}
	}
	return Stat{Avg: sum / int64(len(vals)), P50: Percentile(vals, 0.5), P95: Percentile(vals, 0.95), Peak: peak}
}

func Pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/analyze && go test ./internal/analyze/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/analyze
git commit -m "falcon-resource-test: add stats helpers

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: Health checks

These are pure functions: each takes plain data and returns a `Check`. The collector stores **only Falcon objects** in snapshots (DaemonSet Sensor DaemonSets in `SnapDaemonSets`, Falcon webhooks in `SnapWebhooks`), so the checks don't have to filter again.

Statuses: `pass`, `fail`, `review` (amber, explained, not a failure), `info` (fact, not graded), `not_measured` (gray, with reason).

**Files:**
- Create: `internal/analyze/checks.go`
- Test: `internal/analyze/checks_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

var t0 = time.Unix(1_700_000_100, 0)

func TestCheckStable(t *testing.T) {
	if c := checkStable(nil, true); c.Status != Pass {
		t.Fatalf("no events: %+v", c)
	}
	ev := []model.Event{{Kind: model.EventRestart, Falcon: true, Object: "p1"}, {Kind: model.EventRestart, Falcon: false}}
	if c := checkStable(ev, true); c.Status != Fail || !strings.Contains(c.Summary, "1 restart") {
		t.Fatalf("one falcon restart: %+v", c)
	}
	if c := checkStable(nil, false); c.Status != NotMeasured {
		t.Fatalf("no falcon pods: %+v", c)
	}
}

func TestCheckLane(t *testing.T) {
	specs := []model.ContainerSpec{{Component: model.KAC, Container: "falcon-ac", LimMem: 256 << 20}}
	under := []model.Sample{{Component: model.KAC, Container: "falcon-ac", MemMax: 200 << 20}}
	at := []model.Sample{{Component: model.KAC, Container: "falcon-ac", MemMax: 256 << 20}}
	if c := checkLane(specs, under); c.Status != Pass {
		t.Fatalf("under: %+v", c)
	}
	if c := checkLane(specs, at); c.Status != Fail {
		t.Fatalf("at limit: %+v", c)
	}
	if c := checkLane([]model.ContainerSpec{{Component: model.KAC, Container: "falcon-ac"}}, under); c.Status != Info {
		t.Fatalf("no limits: %+v", c)
	}
	if c := checkLane(specs, nil); c.Status != NotMeasured {
		t.Fatalf("no samples: %+v", c)
	}
}

func TestCheckCrowd(t *testing.T) {
	falconNodes := map[string]float64{"n1": 1.5}
	if c := checkCrowd(nil, falconNodes); c.Status != Pass {
		t.Fatalf("clean: %+v", c)
	}
	evict := []model.Event{{Kind: model.EventEviction, Node: "n1", Falcon: true, Object: "falcon-sensor-x"}}
	if c := checkCrowd(evict, falconNodes); c.Status != Fail {
		t.Fatalf("falcon evicted: %+v", c)
	}
	press := []model.Event{{Kind: model.EventPressure, Node: "n1", Reason: "MemoryPressure"}}
	c := checkCrowd(press, falconNodes)
	if c.Status != Review || !strings.Contains(strings.Join(c.Details, " "), "1.5%") {
		t.Fatalf("pressure: %+v", c)
	}
	// pressure on a node without Falcon is not our concern
	if c := checkCrowd([]model.Event{{Kind: model.EventPressure, Node: "n9"}}, falconNodes); c.Status != Pass {
		t.Fatalf("other node: %+v", c)
	}
}

func TestCheckCoverage(t *testing.T) {
	to := t0.Add(10 * time.Minute)
	full := []TimedDaemonSets{{TS: t0, State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 3}}}}
	if c := checkCoverage(full, t0, to); c.Status != Pass {
		t.Fatalf("full: %+v", c)
	}
	short := append(full, TimedDaemonSets{TS: t0.Add(4 * time.Minute), State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 2}}},
		TimedDaemonSets{TS: t0.Add(6 * time.Minute), State: []model.DaemonSetState{{Name: "falcon-sensor", Desired: 3, Ready: 3}}})
	c := checkCoverage(short, t0, to)
	if c.Status != Fail || !strings.Contains(c.Summary, "2m0s") {
		t.Fatalf("2 minute shortfall: %+v", c)
	}
	if c := checkCoverage(nil, t0, to); c.Status != NotMeasured {
		t.Fatalf("no ds: %+v", c)
	}
}

func TestCheckAdmission(t *testing.T) {
	if c := checkAdmission(nil, false); c.Status != NotMeasured {
		t.Fatalf("no kac: %+v", c)
	}
	ign := []model.WebhookState{{Name: "validating.falcon-kac.crowdstrike.com", FailurePolicy: "Ignore", TimeoutSeconds: 10}}
	if c := checkAdmission(ign, true); c.Status != Pass {
		t.Fatalf("ignore: %+v", c)
	}
	fail := []model.WebhookState{{Name: "x", FailurePolicy: "Fail", TimeoutSeconds: 10}}
	if c := checkAdmission(fail, true); c.Status != Review {
		t.Fatalf("fail policy is review, not fail: %+v", c)
	}
}

func TestCheckSensorHealth(t *testing.T) {
	to := t0
	if c := checkSensorHealth(nil, false, to); c.Status != NotMeasured {
		t.Fatalf("not enabled: %+v", c)
	}
	ok := []model.SensorHost{{Hostname: "n1", Online: true, LastSeen: to.Add(-time.Minute)}}
	if c := checkSensorHealth(ok, true, to); c.Status != Pass {
		t.Fatalf("healthy: %+v", c)
	}
	rfm := []model.SensorHost{{Hostname: "n1", Online: true, RFM: true, LastSeen: to}}
	if c := checkSensorHealth(rfm, true, to); c.Status != Fail {
		t.Fatalf("rfm: %+v", c)
	}
	stale := []model.SensorHost{{Hostname: "n1", Online: true, LastSeen: to.Add(-2 * time.Hour)}}
	if c := checkSensorHealth(stale, true, to); c.Status != Fail {
		t.Fatalf("stale: %+v", c)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/analyze/ -run Check`
Expected: FAIL (`undefined: checkStable`)

- [ ] **Step 3: Implement**

```go
package analyze

import (
	"fmt"
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

type Status string

const (
	Pass        Status = "pass"
	Fail        Status = "fail"
	Review      Status = "review"
	Info        Status = "info"
	NotMeasured Status = "not_measured"
)

type Check struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  Status   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

// TimedDaemonSets is one SnapDaemonSets snapshot (DaemonSet Sensor only).
type TimedDaemonSets struct {
	TS    time.Time
	State []model.DaemonSetState
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func checkStable(events []model.Event, falconSeen bool) Check {
	c := Check{ID: "stable", Title: "Stable"}
	if !falconSeen {
		c.Status, c.Summary = NotMeasured, "No Falcon pods found in this window."
		return c
	}
	counts := map[model.EventKind]int{}
	for _, e := range events {
		if !e.Falcon {
			continue
		}
		switch e.Kind {
		case model.EventRestart, model.EventOOMKill, model.EventCrashLoop:
			counts[e.Kind]++
			c.Details = append(c.Details, fmt.Sprintf("%s %s/%s at %s: %s", e.Kind, e.Namespace, e.Object, e.TS.UTC().Format(time.RFC3339), e.Reason))
		}
	}
	r, o, l := counts[model.EventRestart], counts[model.EventOOMKill], counts[model.EventCrashLoop]
	c.Summary = fmt.Sprintf("%s, %s, %s.", plural(r, "restart"), plural(o, "OOMKill"), plural(l, "CrashLoop"))
	if r+o+l == 0 {
		c.Status = Pass
	} else {
		c.Status = Fail
	}
	return c
}

// checkLane compares each container's peak memory with its limit. Limits
// are keyed by (component, container name) because pods rotate but the
// workload template doesn't.
func checkLane(specs []model.ContainerSpec, samples []model.Sample) Check {
	c := Check{ID: "lane", Title: "Stays in its lane"}
	if len(samples) == 0 {
		c.Status, c.Summary = NotMeasured, "No usage samples in this window (metrics-server unavailable or Falcon not running)."
		return c
	}
	type key struct {
		comp model.Component
		name string
	}
	limMem, limCPU := map[key]int64{}, map[key]int64{}
	for _, s := range specs {
		if s.LimMem > 0 {
			limMem[key{s.Component, s.Container}] = s.LimMem
		}
		if s.LimCPUm > 0 {
			limCPU[key{s.Component, s.Container}] = s.LimCPUm
		}
	}
	if len(limMem) == 0 {
		c.Status, c.Summary = Info, "No memory limits are set on Falcon containers, so there is no ceiling to compare against."
		return c
	}
	peakMem, peakCPU := map[key]int64{}, map[key]int64{}
	for _, s := range samples {
		k := key{s.Component, s.Container}
		if s.MemMax > peakMem[k] {
			peakMem[k] = s.MemMax
		}
		if s.CPUMaxm > peakCPU[k] {
			peakCPU[k] = s.CPUMaxm
		}
	}
	keys := make([]key, 0, len(limMem))
	for k := range limMem {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].comp+model.Component(keys[i].name) < keys[j].comp+model.Component(keys[j].name) })
	hit := 0
	for _, k := range keys {
		lim, peak := limMem[k], peakMem[k]
		c.Details = append(c.Details, fmt.Sprintf("%s / %s: peak %s of %s limit (%.0f%%)", k.comp.Label(), k.name, MiB(peak), MiB(lim), Pct(peak, lim)))
		if peak >= lim {
			hit++
		}
	}
	for k, lim := range limCPU {
		if peakCPU[k]*100 >= lim*95 {
			c.Details = append(c.Details, fmt.Sprintf("%s / %s: CPU peaked at its %dm limit (CPU is throttled at the limit, not killed)", k.comp.Label(), k.name, lim))
		}
	}
	if hit == 0 {
		c.Status, c.Summary = Pass, "Peak memory stayed under the limit for every Falcon container."
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("%s reached its memory limit.", plural(hit, "container"))
	}
	return c
}

// checkCrowd: falconNodes maps node -> Falcon p95 CPU share of that node (%).
func checkCrowd(events []model.Event, falconNodes map[string]float64) Check {
	c := Check{ID: "crowd", Title: "Doesn't crowd workloads", Status: Pass,
		Summary: "No evictions or node pressure on nodes running Falcon."}
	review := 0
	for _, e := range events {
		share, onFalconNode := falconNodes[e.Node]
		switch {
		case e.Kind == model.EventEviction && e.Falcon:
			c.Status = Fail
			c.Details = append(c.Details, fmt.Sprintf("Falcon pod %s/%s was evicted from %s at %s.", e.Namespace, e.Object, e.Node, e.TS.UTC().Format(time.RFC3339)))
		case (e.Kind == model.EventEviction || e.Kind == model.EventPressure) && onFalconNode:
			review++
			c.Details = append(c.Details, fmt.Sprintf("%s on %s at %s (%s). Falcon used %.1f%% of this node's CPU at p95, so the cause is likely another workload.",
				e.Reason, e.Node, e.TS.UTC().Format(time.RFC3339), e.Object, share))
		}
	}
	switch {
	case c.Status == Fail:
		c.Summary = "A Falcon pod was evicted."
	case review > 0:
		c.Status, c.Summary = Review, fmt.Sprintf("%s on nodes running Falcon. Check whether Falcon contributed.", plural(review, "pressure or eviction event"))
	}
	return c
}

// checkCoverage walks the DaemonSet Sensor snapshot timeline and totals the
// time any sensor DaemonSet had fewer Ready pods than desired.
func checkCoverage(snaps []TimedDaemonSets, from, to time.Time) Check {
	c := Check{ID: "coverage", Title: "Full coverage"}
	if len(snaps) == 0 {
		c.Status, c.Summary = NotMeasured, "No DaemonSet Sensor found."
		return c
	}
	var short time.Duration
	for i, s := range snaps {
		start, end := s.TS, to
		if start.Before(from) {
			start = from
		}
		if i+1 < len(snaps) {
			end = snaps[i+1].TS
		}
		for _, d := range s.State {
			if d.Ready < d.Desired && end.After(start) {
				short += end.Sub(start)
				c.Details = append(c.Details, fmt.Sprintf("%s/%s: %d of %d Ready from %s for %s", d.Namespace, d.Name, d.Ready, d.Desired, start.UTC().Format(time.RFC3339), end.Sub(start)))
				break
			}
		}
	}
	last := snaps[len(snaps)-1].State
	var ready, desired int32
	for _, d := range last {
		ready += d.Ready
		desired += d.Desired
	}
	if short == 0 {
		c.Status, c.Summary = Pass, fmt.Sprintf("Every eligible node had a Ready sensor the whole time (%d/%d).", ready, desired)
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("Sensor coverage dropped below 100%% for %s. Now %d/%d.", short, ready, desired)
	}
	return c
}

func checkAdmission(hooks []model.WebhookState, kacPresent bool) Check {
	c := Check{ID: "admission", Title: "Safe admission control"}
	if !kacPresent && len(hooks) == 0 {
		c.Status, c.Summary = NotMeasured, "KAC is not installed."
		return c
	}
	if len(hooks) == 0 {
		c.Status, c.Summary = NotMeasured, "KAC is running but no Falcon webhook configuration was found."
		return c
	}
	c.Status, c.Summary = Pass, "If KAC is unavailable, deployments continue (failurePolicy: Ignore)."
	for _, h := range hooks {
		c.Details = append(c.Details, fmt.Sprintf("%s (%s): failurePolicy %s, timeout %ds", h.Name, h.Kind, h.FailurePolicy, h.TimeoutSeconds))
		if h.FailurePolicy == "Fail" {
			c.Status = Review
			c.Summary = "A Falcon webhook uses failurePolicy: Fail. If KAC is down, matching requests are rejected until it recovers. This is a deliberate choice some teams make to enforce policy; confirm it is intended."
		}
	}
	return c
}

const staleAfter = time.Hour

func checkSensorHealth(hosts []model.SensorHost, known bool, to time.Time) Check {
	c := Check{ID: "sensor", Title: "Sensor healthy"}
	if !known {
		c.Status, c.Summary = NotMeasured, "Falcon API not enabled. Add an API client to see per-node sensor status."
		return c
	}
	bad := 0
	for _, h := range hosts {
		switch {
		case h.RFM:
			bad++
			c.Details = append(c.Details, fmt.Sprintf("%s: reduced functionality mode (sensor %s)", h.Hostname, h.Version))
		case !h.Online || to.Sub(h.LastSeen) > staleAfter:
			bad++
			c.Details = append(c.Details, fmt.Sprintf("%s: last seen %s", h.Hostname, h.LastSeen.UTC().Format(time.RFC3339)))
		}
	}
	if bad == 0 {
		c.Status, c.Summary = Pass, fmt.Sprintf("All %d sensors online, none in reduced functionality mode.", len(hosts))
	} else {
		c.Status, c.Summary = Fail, fmt.Sprintf("%s offline, stale, or in reduced functionality mode.", plural(bad, "sensor"))
	}
	return c
}

func MiB(b int64) string { return fmt.Sprintf("%dMi", b>>20) }
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/analyze && go test ./internal/analyze/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/analyze
git commit -m "falcon-resource-test: add health checks

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: Analyze (report model and headline)

`Analyze` is a pure function from an `Input` (already loaded from the store) to a `Report`. Loading from the store lives in `Load` (same file, thin) so the math stays testable without SQLite.

The collector writes all pod and node samples from one tick with the **same `TS`**. `Analyze` relies on that to join a pod sample to its node sample.

**Files:**
- Create: `internal/analyze/analyze.go`
- Test: `internal/analyze/analyze_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
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
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/analyze/ -run 'Analyze|Human'`
Expected: FAIL (`undefined: Input`)

- [ ] **Step 3: Implement**

```go
package analyze

import (
	"fmt"
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

type Input struct {
	From, To    time.Time
	Interval    time.Duration
	Samples     []model.Sample
	NodeSamples []model.NodeSample
	Events      []model.Event
	DaemonSets  []TimedDaemonSets
	Webhooks    []model.WebhookState  // latest Falcon webhook state
	Specs       []model.ContainerSpec // latest Falcon container specs
	Sensors     []model.SensorHost
	SensorsKnown bool // Falcon API enabled and answered
	KACPresent  bool
}

type ComponentStat struct {
	Component model.Component `json:"component"`
	Label     string          `json:"label"`
	Pods      int             `json:"pods"`
	CPU       Stat            `json:"cpu"` // millicores, summed across pods
	Mem       Stat            `json:"mem"` // bytes, summed across pods
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
	Window          time.Duration `json:"window"`
	ClusterCPUPct   float64       `json:"cluster_cpu_pct"` // all workloads
	Idle            bool          `json:"idle"`
	Gaps            time.Duration `json:"gaps"`
	Notes           []string      `json:"notes"`
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
```

For windows over 2 hours, p95 and peak come from 5-minute bucket averages and maxima, not from raw 15s samples. Peak stays exact; p95 is slightly smoothed. That trade keeps a 24h report on a 50-node cluster inside the memory budget.

`SnapPodSpecs` holds one entry **per workload container**, not per pod (the collector dedupes by component + container name), so `ReqCPUm × Pods` gives the reserved total.

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/analyze && go test ./internal/analyze/`
Expected: `ok`. Check the memory math in `TestAnalyzeHeadlineAndCluster`: Falcon memory per tick is 800 MiB out of 16 GiB, which is 4.88%, so the headline shows "4.9%".

- [ ] **Step 5: Commit**

```bash
git add internal/analyze
git commit -m "falcon-resource-test: add report analysis and headline

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
