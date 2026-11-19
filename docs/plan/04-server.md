# Part 4: Server (Tasks 12–16)

All commands run from ``.

---

### Task 12: Assessment manager

- A scan assessment is a bookmarked time window `[start, start+duration)`. No extra collection happens, because the collector is always recording.
- On startup, `Recover` marks any assessment still `running` as `interrupted`, since the tool restarted mid-window and that window has a hole in it.
- `Refresh` flips `running` to `complete` once `ends_at` has passed. The HTTP layer calls it before it reads assessments, and so does a 15s loop in main.
- **On completion, the report is frozen.** The manager calls an injected `build(from, to)` and stores the resulting JSON with `SetAssessmentResult`, so a saved report outlives the 24h raw-sample retention. History reports freeze right away.

**Files:**
- Create: `internal/assess/manager.go`
- Test: `internal/assess/manager_test.go`

- [ ] **Step 1: Write the failing test**

```go
package assess

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

var t0 = time.Unix(1_700_000_100, 0)

func TestManager(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "fp.db"))
	defer st.Close()
	now := t0
	builds := 0
	build := func(from, to time.Time) ([]byte, error) {
		builds++
		return []byte(`{"from":` + strconv.FormatInt(from.Unix(), 10) + `}`), nil
	}
	m := New(st, func() time.Time { return now }, build)

	if _, err := m.Start(7 * time.Minute); !errors.Is(err, ErrDuration) {
		t.Fatalf("only allowed durations: %v", err)
	}
	a, err := m.Start(5 * time.Minute)
	if err != nil || a.Status != store.StatusRunning {
		t.Fatalf("start: %+v %v", a, err)
	}
	if _, err := m.Start(5 * time.Minute); !errors.Is(err, ErrRunning) {
		t.Fatalf("second start should fail: %v", err)
	}
	now = t0.Add(5 * time.Minute)
	m.Refresh()
	got, _, _ := st.GetAssessment(a.ID)
	if got.Status != store.StatusComplete {
		t.Fatalf("should complete at ends_at: %+v", got)
	}
	if res, ok, _ := st.AssessmentResult(a.ID); !ok || string(res) != `{"from":1700000100}` || builds != 1 {
		t.Fatalf("completion should freeze the report once: %q builds=%d", res, builds)
	}
	m.Refresh()
	if builds != 1 {
		t.Fatal("refresh must not rebuild a completed report")
	}

	if _, err := m.History(30 * time.Second); !errors.Is(err, ErrLookback) {
		t.Fatalf("lookback under 1m: %v", err)
	}
	h, err := m.History(6 * time.Hour)
	if err != nil || h.Kind != store.KindHistory || !h.EndsAt.Equal(now) || !h.StartedAt.Equal(now.Add(-6*time.Hour)) {
		t.Fatalf("history: %+v %v", h, err)
	}
	if _, ok, _ := st.AssessmentResult(h.ID); !ok {
		t.Fatal("history report should be frozen on creation")
	}

	b, _ := m.Start(15 * time.Minute)
	m2 := New(st, func() time.Time { return now.Add(time.Minute) }, build) // simulated restart
	m2.Recover()
	got, _, _ = st.GetAssessment(b.ID)
	if got.Status != store.StatusInterrupted {
		t.Fatalf("restart mid-run should interrupt: %+v", got)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/assess/`
Expected: FAIL (`undefined: New`)

- [ ] **Step 3: Implement**

```go
// Package assess manages assessment windows. An assessment is a bookmarked
// time range over data the collector is already recording.
package assess

import (
	"errors"
	"sync"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

var (
	ErrRunning  = errors.New("an assessment is already running")
	ErrDuration = errors.New("duration must be one of 5m, 15m, 1h, 24h")
	ErrLookback = errors.New("lookback must be between 1 minute and 30 days")
)

// Durations offered by the UI. Test builds may add shorter ones via Allow.
var Durations = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 24 * time.Hour}

// BuildFunc renders the frozen report JSON for a window.
type BuildFunc func(from, to time.Time) ([]byte, error)

type Manager struct {
	st    *store.Store
	now   func() time.Time
	build BuildFunc
	mu    sync.Mutex
	allow map[time.Duration]bool
}

func New(st *store.Store, now func() time.Time, build BuildFunc) *Manager {
	m := &Manager{st: st, now: now, build: build, allow: map[time.Duration]bool{}}
	for _, d := range Durations {
		m.allow[d] = true
	}
	return m
}

// Allow adds an extra permitted duration (used by e2e tests: 1m).
func (m *Manager) Allow(d time.Duration) { m.allow[d] = true }

// Recover runs once at startup. Anything still "running" was cut short by a
// restart (or ended while we were down), so its window has a hole.
func (m *Manager) Recover() error {
	run, ok, err := m.st.RunningAssessment()
	if err != nil || !ok {
		return err
	}
	return m.st.SetAssessmentStatus(run.ID, store.StatusInterrupted)
}

func (m *Manager) Start(d time.Duration) (store.Assessment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.allow[d] {
		return store.Assessment{}, ErrDuration
	}
	m.refreshLocked()
	if _, ok, err := m.st.RunningAssessment(); err != nil {
		return store.Assessment{}, err
	} else if ok {
		return store.Assessment{}, ErrRunning
	}
	now := m.now().Truncate(time.Second)
	return m.st.CreateAssessment(store.KindScan, now, now.Add(d), now)
}

// History creates an instant report over the last d. The report is built
// before the row is written, so a failed build leaves nothing behind.
func (m *Manager) History(d time.Duration) (store.Assessment, error) {
	if d < time.Minute || d > 30*24*time.Hour {
		return store.Assessment{}, ErrLookback
	}
	now := m.now().Truncate(time.Second)
	b, err := m.build(now.Add(-d), now)
	if err != nil {
		return store.Assessment{}, err
	}
	a, err := m.st.CreateAssessment(store.KindHistory, now.Add(-d), now, now)
	if err != nil {
		return a, err
	}
	return a, m.st.SetAssessmentResult(a.ID, b)
}

func (m *Manager) freeze(a store.Assessment) error {
	b, err := m.build(a.StartedAt, a.EndsAt)
	if err != nil {
		return err
	}
	return m.st.SetAssessmentResult(a.ID, b)
}

func (m *Manager) Refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
}

func (m *Manager) refreshLocked() {
	run, ok, err := m.st.RunningAssessment()
	if err != nil || !ok || m.now().Before(run.EndsAt) {
		return
	}
	// Freeze first: if building fails, it stays running and the next
	// Refresh retries, so a "complete" assessment always has a result.
	if m.freeze(run) == nil {
		m.st.SetAssessmentStatus(run.ID, store.StatusComplete)
	}
}
```

- [ ] **Step 4: Run the test**

Run: `gofmt -w internal/assess && go test ./internal/assess/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/assess
git commit -m "falcon-resource-test: add assessment manager

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 13: Self-contained HTML report

Server-rendered with `html/template`: inline CSS, inline SVG charts, no JS, no external URLs. It uses the brand palette (dark header band, `#ED1C24` accent) on a light page so it prints cleanly.

**Files:**
- Create: `internal/report/svg.go`
- Create: `internal/report/report.go`
- Create: `internal/report/report.html.tmpl`
- Test: `internal/report/report_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/analyze"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

func TestAreaPath(t *testing.T) {
	got := AreaPath([]int64{0, 10, 5}, 100, 50, 10)
	want := "M0,50 L0,50 L50,0 L100,25 L100,50 Z"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if AreaPath(nil, 100, 50, 0) != "" {
		t.Fatal("empty series")
	}
}

func TestRenderSelfContained(t *testing.T) {
	t0 := time.Unix(1_700_000_100, 0)
	rep := analyze.Report{
		From: t0, To: t0.Add(15 * time.Minute),
		Headline:   "Over 15 minutes on 2 nodes, Falcon used 2.3% of cluster CPU <and> 4.9% of memory.",
		Components: []analyze.ComponentStat{{Component: model.DaemonSetSensor, Label: "DaemonSet Sensor", Pods: 2, CPU: analyze.Stat{Avg: 80}}},
		Checks:     []analyze.Check{{ID: "stable", Title: "Stable", Status: analyze.Pass, Summary: "0 restarts."}},
		Method:     analyze.Methodology{Notes: []string{"note one"}},
	}
	series := map[model.Component][]store.Point{model.DaemonSetSensor: {{TS: t0.Unix(), CPUm: 80, MemBytes: 1 << 20}}}
	var buf bytes.Buffer
	if err := Render(&buf, Data{Cluster: "prod-east-1", Bundle: Bundle{Report: rep, Series: series}, Generated: t0, Version: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"prod-east-1", "DaemonSet Sensor", "&lt;and&gt;", "note one", "<svg", "check--pass"} {
		if !strings.Contains(html, want) {
			t.Errorf("report missing %q", want)
		}
	}
	for _, banned := range []string{"<script", "http://", "https://", "src=\"/"} {
		if strings.Contains(html, banned) {
			t.Errorf("report must be self-contained, found %q", banned)
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/report/`
Expected: FAIL (`undefined: AreaPath`)

- [ ] **Step 3: Implement `svg.go`**

```go
package report

import (
	"fmt"
	"strings"
)

// AreaPath returns an SVG path for an area chart of vals scaled into a
// w x h box with max as the top of the y axis.
func AreaPath(vals []int64, w, h float64, max int64) string {
	if len(vals) == 0 || max <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M0,%g", h)
	step := w / float64(max64(int64(len(vals)-1), 1))
	for i, v := range vals {
		fmt.Fprintf(&b, " L%g,%g", float64(i)*step, h-float64(v)*h/float64(max))
	}
	fmt.Fprintf(&b, " L%g,%g Z", float64(len(vals)-1)*step, h)
	return b.String()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Implement `report.go`**

```go
// Package report renders an analysis as one self-contained HTML file.
package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/analyze"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

//go:embed report.html.tmpl
var tmplSrc string

// Bundle is everything a saved report needs. It is what gets frozen into
// assessments.result, so exports keep working after raw data expires.
type Bundle struct {
	Report analyze.Report                    `json:"report"`
	Series map[model.Component][]store.Point `json:"series"`
}

// Build analyzes [from, to) and attaches chart series at ~400 points.
func Build(st *store.Store, from, to time.Time, interval time.Duration, kacPresent, sensorsKnown bool) (Bundle, error) {
	in, err := analyze.Load(st, from, to, interval, kacPresent, sensorsKnown)
	if err != nil {
		return Bundle{}, err
	}
	series, err := st.ComponentSeries(from, to, store.StepFor(to.Sub(from)), "")
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Report: analyze.Analyze(in), Series: series}, nil
}

type Data struct {
	Cluster string
	Bundle
	Generated time.Time
	Version   string
}

type chart struct {
	Title, Unit string
	Max         string
	Layers      []layer
}

type layer struct {
	Label, Color, Path string
}

var palette = map[model.Component]string{
	model.DaemonSetSensor: "#ED1C24",
	model.KAC:             "#5b9cf5",
	model.IAR:             "#3ecfba",
	model.Injector:        "#f0a830",
	model.Operator:        "#8f8a9e",
}

var tmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"cpu":  func(m int64) string { return fmt.Sprintf("%dm", m) },
	"mem":  analyze.MiB,
	"pct":  func(p float64) string { return fmt.Sprintf("%.1f%%", p) },
	"ts":   func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
	"dur":  analyze.HumanDuration,
}).Parse(tmplSrc))

// stacked builds cumulative layers so areas stack bottom-up in display order.
// Points are aligned by timestamp; a component missing a bucket adds 0 there.
func stacked(series map[model.Component][]store.Point, val func(store.Point) int64) ([]layer, int64) {
	var comps []model.Component
	seen := map[int64]bool{}
	var ts []int64
	for _, c := range model.FalconComponents {
		if len(series[c]) == 0 {
			continue
		}
		comps = append(comps, c)
		for _, p := range series[c] {
			if !seen[p.TS] {
				seen[p.TS] = true
				ts = append(ts, p.TS)
			}
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	idx := make(map[int64]int, len(ts))
	for i, t := range ts {
		idx[t] = i
	}
	cum := make([]int64, len(ts))
	var cums [][]int64
	for _, c := range comps {
		for _, p := range series[c] {
			cum[idx[p.TS]] += val(p)
		}
		cums = append(cums, append([]int64(nil), cum...))
	}
	var max int64
	for _, v := range cum {
		if v > max {
			max = v
		}
	}
	var layers []layer
	for i := len(comps) - 1; i >= 0; i-- { // draw tallest first
		layers = append(layers, layer{Label: comps[i].Label(), Color: palette[comps[i]], Path: AreaPath(cums[i], 720, 160, max)})
	}
	return layers, max
}

func Render(w io.Writer, d Data) error {
	cpuLayers, cpuMax := stacked(d.Series, func(p store.Point) int64 { return p.CPUm })
	memLayers, memMax := stacked(d.Series, func(p store.Point) int64 { return p.MemBytes })
	return tmpl.Execute(w, map[string]any{
		"D": d,
		"R": d.Report,
		"Charts": []chart{
			{Title: "Falcon CPU", Unit: "millicores", Max: fmt.Sprintf("%dm", cpuMax), Layers: cpuLayers},
			{Title: "Falcon memory", Unit: "working set", Max: analyze.MiB(memMax), Layers: memLayers},
		},
	})
}
```

- [ ] **Step 5: Implement `report.html.tmpl`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Falcon Resource Test Report: {{.D.Cluster}}</title>
<style>
  :root { --red:#ED1C24; --ink:#1a1724; --muted:#6b6675; --line:#e5e0d8; --bg:#f8f6f3; --card:#fff;
          --pass:#1f9d55; --fail:#ED1C24; --review:#c27c0e; --info:#3b6fd4; --nm:#8f8a9e; }
  * { box-sizing:border-box; margin:0; padding:0; }
  body { font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,sans-serif; color:var(--ink); background:var(--bg); }
  header { background:#171520; color:#f0ecfa; padding:28px 40px; border-bottom:4px solid var(--red); }
  header .brand { font-size:12px; letter-spacing:.14em; text-transform:uppercase; color:#8f8a9e; }
  header h1 { font-size:22px; margin-top:4px; }
  header .meta { font-size:12px; color:#8f8a9e; margin-top:6px; }
  main { max-width:960px; margin:0 auto; padding:32px 40px 64px; }
  .headline { font-size:20px; font-weight:600; background:var(--card); border-left:4px solid var(--red); padding:20px 24px; border-radius:8px; }
  h2 { font-size:15px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); margin:36px 0 12px; }
  .tiles { display:grid; grid-template-columns:repeat(4,1fr); gap:12px; }
  .tile { background:var(--card); border:1px solid var(--line); border-radius:8px; padding:14px 16px; }
  .tile .v { font-size:22px; font-weight:700; }
  .tile .l { font-size:12px; color:var(--muted); }
  table { width:100%; border-collapse:collapse; background:var(--card); border:1px solid var(--line); border-radius:8px; overflow:hidden; }
  th, td { text-align:left; padding:8px 12px; border-bottom:1px solid var(--line); font-size:13px; }
  th { background:#f4f2ef; font-weight:600; color:var(--muted); }
  td.n { text-align:right; font-variant-numeric:tabular-nums; }
  .check { background:var(--card); border:1px solid var(--line); border-left:4px solid var(--nm); border-radius:8px; padding:12px 16px; margin-bottom:8px; }
  .check--pass { border-left-color:var(--pass); } .check--fail { border-left-color:var(--fail); }
  .check--review { border-left-color:var(--review); } .check--info { border-left-color:var(--info); }
  .check .s { font-size:11px; font-weight:700; text-transform:uppercase; letter-spacing:.08em; }
  .check--pass .s { color:var(--pass); } .check--fail .s { color:var(--fail); } .check--review .s { color:var(--review); }
  .check--info .s { color:var(--info); } .check--not_measured .s { color:var(--nm); }
  .check ul { margin:6px 0 0 18px; font-size:12px; color:var(--muted); }
  .chart { background:var(--card); border:1px solid var(--line); border-radius:8px; padding:16px; margin-bottom:12px; }
  .chart h3 { font-size:13px; margin-bottom:8px; } .chart .max { font-size:11px; color:var(--muted); float:right; }
  .legend span { display:inline-block; margin-right:14px; font-size:12px; color:var(--muted); }
  .legend i { display:inline-block; width:10px; height:10px; border-radius:2px; margin-right:5px; vertical-align:-1px; }
  .notes li { margin:0 0 6px 18px; font-size:13px; color:var(--muted); }
  footer { font-size:11px; color:var(--muted); text-align:center; margin-top:40px; }
  @media print { body { background:#fff; } header { -webkit-print-color-adjust:exact; print-color-adjust:exact; } .chart, .check, table { break-inside:avoid; } }
</style>
</head>
<body>
<header>
  <div class="brand">CrowdStrike Falcon Resource Test</div>
  <h1>Production Readiness Report: {{.D.Cluster}}</h1>
  <div class="meta">{{ts .R.From}} to {{ts .R.To}} ({{dur .R.Method.Window}}) · generated {{ts .D.Generated}} · v{{.D.Version}}</div>
</header>
<main>
  <p class="headline">{{.R.Headline}}</p>

  <h2>What it costs</h2>
  <div class="tiles">
    <div class="tile"><div class="v">{{pct .R.Cluster.CPUPct}}</div><div class="l">of cluster CPU used (avg {{cpu .R.Cluster.CPU.Avg}})</div></div>
    <div class="tile"><div class="v">{{pct .R.Cluster.MemPct}}</div><div class="l">of cluster memory used (avg {{mem .R.Cluster.Mem.Avg}})</div></div>
    <div class="tile"><div class="v">{{pct .R.Cluster.ReservedCPUPct}}</div><div class="l">of cluster CPU reserved (requests)</div></div>
    <div class="tile"><div class="v">{{pct .R.Cluster.ReservedMemPct}}</div><div class="l">of cluster memory reserved (requests)</div></div>
  </div>

  <h2>Usage over time</h2>
  {{range .Charts}}
  <div class="chart">
    <h3>{{.Title}} <span class="max">peak {{.Max}}</span></h3>
    <svg viewBox="0 0 720 160" width="100%" height="160" preserveAspectRatio="none" role="img" aria-label="{{.Title}}">
      {{range .Layers}}<path d="{{.Path}}" fill="{{.Color}}" fill-opacity="0.85"/>{{end}}
    </svg>
    <div class="legend">{{range .Layers}}<span><i style="background:{{.Color}}"></i>{{.Label}}</span>{{end}}</div>
  </div>
  {{end}}

  <h2>By component</h2>
  <table>
    <tr><th>Component</th><th>Pods</th><th>CPU avg</th><th>CPU p95</th><th>CPU peak</th><th>Mem avg</th><th>Mem p95</th><th>Mem peak</th><th>Reserved CPU / Mem</th></tr>
    {{range .R.Components}}
    <tr><td>{{.Label}}</td><td class="n">{{.Pods}}</td>
      <td class="n">{{cpu .CPU.Avg}}</td><td class="n">{{cpu .CPU.P95}}</td><td class="n">{{cpu .CPU.Peak}}</td>
      <td class="n">{{mem .Mem.Avg}}</td><td class="n">{{mem .Mem.P95}}</td><td class="n">{{mem .Mem.Peak}}</td>
      <td class="n">{{cpu .ReqCPUm}} / {{mem .ReqMem}}</td></tr>
    {{end}}
  </table>

  <h2>By node (worst first)</h2>
  <table>
    <tr><th>Node</th><th>Falcon CPU p95</th><th>Share of node CPU</th><th>Falcon mem p95</th><th>Share of node memory</th></tr>
    {{range .R.Nodes}}
    <tr><td>{{.Node}}</td><td class="n">{{cpu .CPU.P95}}</td><td class="n">{{pct .CPUShareP95}}</td><td class="n">{{mem .Mem.P95}}</td><td class="n">{{pct .MemShareP95}}</td></tr>
    {{end}}
  </table>

  <h2>Health checks</h2>
  {{range .R.Checks}}
  <div class="check check--{{.Status}}">
    <div class="s">{{.Status}}</div>
    <strong>{{.Title}}</strong>: {{.Summary}}
    {{if .Details}}<ul>{{range .Details}}<li>{{.}}</li>{{end}}</ul>{{end}}
  </div>
  {{end}}

  <h2>How it's configured</h2>
  <table>
    <tr><th>Component</th><th>Container</th><th>Version</th><th>Backend</th><th>Requests</th><th>Limits</th><th>Priority class</th></tr>
    {{range .R.Config}}
    <tr><td>{{.Component.Label}}</td><td>{{.Container}}</td><td>{{.Version}}</td><td>{{.Backend}}</td>
      <td>{{cpu .ReqCPUm}} / {{mem .ReqMem}}</td>
      <td>{{if .LimCPUm}}{{cpu .LimCPUm}}{{else}}none{{end}} / {{if .LimMem}}{{mem .LimMem}}{{else}}none{{end}}</td>
      <td>{{.PriorityClass}}</td></tr>
    {{end}}
  </table>

  <h2>How to read this</h2>
  <ul class="notes">{{range .R.Method.Notes}}<li>{{.}}</li>{{end}}</ul>

  <footer>Generated by Falcon Resource Test, a read-only SE-built tool. Not an official CrowdStrike product.</footer>
</main>
</body>
</html>
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -w internal/report && go test ./internal/report/`
Expected: `ok`. Inline SVG in HTML needs no `xmlns` attribute; don't add one, or the self-contained test will match `http://`.

- [ ] **Step 7: Commit**

```bash
git add internal/report
git commit -m "falcon-resource-test: add self-contained HTML report

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```


---

### Task 14: Preflight checks (`/setup`)

Each RBAC permission the tool uses is checked with a `SelfSubjectAccessReview`, which works for any authenticated user. metrics-server is probed with one node-metrics call. The results are plain data; the UI renders them and adds install hints.

**Files:**
- Create: `internal/setup/setup.go`
- Test: `internal/setup/setup_test.go`

- [ ] **Step 1: Write the failing test**

```go
package setup

import (
	"context"
	"errors"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type probe struct{ err error }

func (p probe) Probe(context.Context) error { return p.err }

func TestRun(t *testing.T) {
	cs := fake.NewSimpleClientset()
	// deny only events/list
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview)
		r.Status.Allowed = !(r.Spec.ResourceAttributes.Resource == "events" && r.Spec.ResourceAttributes.Verb == "list")
		return true, r, nil
	})

	res := Run(context.Background(), cs.AuthorizationV1(), probe{errors.New("the server could not find the requested resource")}, 2)
	if res.Metrics.OK || res.Metrics.Hint == "" {
		t.Fatalf("metrics-server missing should fail with a hint: %+v", res.Metrics)
	}
	var denied []string
	for _, p := range res.Permissions {
		if !p.Allowed {
			denied = append(denied, p.Verb+" "+p.Resource)
		}
	}
	if len(denied) != 1 || denied[0] != "list events" {
		t.Fatalf("denied = %v", denied)
	}
	if !res.FalconFound || res.OK {
		t.Fatalf("falcon found but overall not OK: %+v", res)
	}

	res = Run(context.Background(), fake.NewSimpleClientset().AuthorizationV1(), probe{}, 0)
	if res.FalconFound {
		t.Fatal("0 workloads = not found")
	}
}
```

The default fake answers SSARs with `Allowed: false`. The second `Run` only checks `FalconFound`, so that's fine.

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/setup/`
Expected: FAIL (`undefined: Run`)

- [ ] **Step 3: Implement**

```go
// Package setup runs the preflight checks shown on the Setup page.
package setup

import (
	"context"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authzv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

type Permission struct {
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Allowed  bool   `json:"allowed"`
	Used     string `json:"used_for"`
}

type MetricsCheck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

type Result struct {
	OK          bool         `json:"ok"`
	Metrics     MetricsCheck `json:"metrics"`
	Permissions []Permission `json:"permissions"`
	FalconFound bool         `json:"falcon_found"`
	CheckedAt   time.Time    `json:"checked_at"`
}

// Prober is a cheap metrics.k8s.io call (one NodeMetrics list).
type Prober interface {
	Probe(ctx context.Context) error
}

// Required mirrors the ClusterRole in the Helm chart. Keep them in sync.
var Required = []Permission{
	{"", "pods", "list", false, "Falcon pod status, restarts, specs"},
	{"", "nodes", "list", false, "allocatable capacity, pressure"},
	{"", "events", "list", false, "evictions"},
	{"apps", "daemonsets", "list", false, "discovery, node coverage"},
	{"apps", "deployments", "list", false, "discovery"},
	{"admissionregistration.k8s.io", "mutatingwebhookconfigurations", "list", false, "KAC failure policy"},
	{"admissionregistration.k8s.io", "validatingwebhookconfigurations", "list", false, "KAC failure policy"},
	{"metrics.k8s.io", "pods", "list", false, "Falcon CPU and memory"},
	{"metrics.k8s.io", "nodes", "list", false, "cluster load"},
}

const metricsHint = "metrics-server is not installed or not ready. Install it with: kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml (EKS: enable the metrics-server add-on; GKE and AKS ship it by default)."

func Run(ctx context.Context, authz authzv1client.AuthorizationV1Interface, metrics Prober, falconWorkloads int) Result {
	res := Result{CheckedAt: time.Now(), FalconFound: falconWorkloads > 0, OK: true}

	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := metrics.Probe(mctx)
	cancel()
	res.Metrics.OK = err == nil
	if err != nil {
		res.Metrics.Error, res.Metrics.Hint = err.Error(), metricsHint
		res.OK = false
	}

	for _, p := range Required {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		r, err := authz.SelfSubjectAccessReviews().Create(cctx, &authzv1.SelfSubjectAccessReview{
			Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authzv1.ResourceAttributes{
				Group: p.Group, Resource: p.Resource, Verb: p.Verb,
			}},
		}, metav1.CreateOptions{})
		cancel()
		p.Allowed = err == nil && r.Status.Allowed
		if !p.Allowed {
			res.OK = false
		}
		res.Permissions = append(res.Permissions, p)
	}
	if !res.FalconFound {
		res.OK = false
	}
	return res
}
```

- [ ] **Step 4: Add `Probe` to the real metrics source** (append to `internal/collect/sources.go`)

```go
// Probe makes one cheap NodeMetrics call; used by /setup.
func (m metricsAPI) Probe(ctx context.Context) error {
	_, err := m.c.NodeMetricses().List(ctx, metav1.ListOptions{Limit: 1})
	return err
}
```

Also add `Probe(ctx context.Context) error` to the `MetricsSource` interface, plus a matching method on the test fake in `collector_test.go`:

```go
func (f *fakeMetrics) Probe(context.Context) error { return f.err }
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -w internal/setup internal/collect && go test ./internal/setup/ ./internal/collect/`
Expected: `ok` for both.

- [ ] **Step 6: Commit**

```bash
git add internal/setup internal/collect
git commit -m "falcon-resource-test: add preflight checks

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 15: HTTP API

Uses Go 1.22+ `http.ServeMux` method patterns, with no router dependency. `/api/live` is cached for 10s, so several open tabs cost the same as one. The UI polls only while visible. Static assets are pre-gzipped at build time and served compressed, so the binary doesn't compress anything per request.

**Files:**
- Create: `internal/web/live.go` (the live view model)
- Create: `internal/web/server.go` (handlers)
- Create: `internal/web/embed.go` (static files plus SPA fallback)
- Create: `internal/web/dist/.gitkeep`
- Test: `internal/web/server_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/assess"
	"github.com/vianneyp72/falcon-resource-test/internal/collect"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/report"
	"github.com/vianneyp72/falcon-resource-test/internal/setup"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

var t0 = time.Unix(1_700_000_100, 0)

type fakeStatus struct{ s collect.Status }

func (f *fakeStatus) Status() collect.Status { return f.s }

func newTestServer(t *testing.T) (*Server, *store.Store, *fakeStatus, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "fp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := t0.Add(10 * time.Minute)
	clock := func() time.Time { return now }
	fs := &fakeStatus{}
	build := func(from, to time.Time) ([]byte, error) {
		b, err := report.Build(st, from, to, 15*time.Second, false, false)
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	}
	srv := New(Deps{
		Store: st, Collector: fs, Assess: assess.New(st, clock, build),
		Setup:    func(context.Context) setup.Result { return setup.Result{OK: true} },
		Cluster:  "kind-test", Version: "0.0.0-test", Interval: 15 * time.Second, Now: clock,
		Static: fstest.MapFS{
			"index.html":       {Data: []byte("<!doctype html><div id=root></div>")},
			"assets/app.js":    {Data: []byte("console.log(1)")},
			"assets/app.js.gz": {Data: []byte("gz")},
		},
	})
	return srv, st, fs, &now
}

// 40 ticks of one sensor pod on one 2-CPU node, plus the tool itself
func seedLive(t *testing.T, st *store.Store, fs *fakeStatus) {
	var samples []model.Sample
	var nodes []model.NodeSample
	for i := 0; i < 40; i++ {
		ts := t0.Add(time.Duration(i) * 15 * time.Second)
		samples = append(samples,
			model.Sample{TS: ts, Node: "n1", Namespace: "falcon-system", Pod: "s1", Container: "falcon-node-sensor", Component: model.DaemonSetSensor, CPUm: 40, MemBytes: 200 << 20, CPUMaxm: 40, MemMax: 200 << 20},
			model.Sample{TS: ts, Node: "n1", Namespace: "falcon-resource-test", Pod: "fp-0", Container: "falcon-resource-test", Component: model.Self, CPUm: 3, MemBytes: 30 << 20, CPUMaxm: 3, MemMax: 30 << 20})
		nodes = append(nodes, model.NodeSample{TS: ts, Node: "n1", AllocCPUm: 2000, AllocMem: 4 << 30, UsedCPUm: 500, UsedMem: 1 << 30})
	}
	st.InsertSamples(samples)
	st.InsertNodeSamples(nodes)
	st.PutSnapshot(t0, model.SnapDaemonSets, []model.DaemonSetState{{Namespace: "falcon-system", Name: "falcon-sensor", Desired: 1, Ready: 1, Updated: 1}})
	st.PutSnapshot(t0, model.SnapPodSpecs, []model.ContainerSpec{{Component: model.DaemonSetSensor, Container: "falcon-node-sensor", ReqCPUm: 250, ReqMem: 512 << 20}})
	st.InsertEvent(model.Event{TS: t0.Add(time.Minute), Kind: model.EventRestart, Namespace: "falcon-system", Object: "s1", Falcon: true})
	fs.s = collect.Status{
		LastTick: t0.Add(585 * time.Second), MetricsOK: true, Nodes: 1, Discovery: discoverResult(),
		Pods:        []collect.PodState{{Component: model.DaemonSetSensor, Namespace: "falcon-system", Name: "s1", Node: "n1", Ready: true, Phase: "Running"}},
		Latest:      samples[len(samples)-2:],
		LatestNodes: nodes[len(nodes)-1:],
	}
}

func get(t *testing.T, srv http.Handler, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body)
		}
	}
	return rec
}

func TestLive(t *testing.T) {
	srv, st, fs, _ := newTestServer(t)
	seedLive(t, st, fs)
	var live Live
	get(t, srv, "/api/live", &live)
	if live.Cluster != "kind-test" || !live.MetricsOK || !live.FalconFound {
		t.Fatalf("live header: %+v", live)
	}
	if live.KPI.CPUm != 40 || live.KPI.CPUPct != 2 || live.KPI.Restarts24h != 1 || live.KPI.Ready != 1 || live.KPI.Desired != 1 {
		t.Fatalf("kpi: %+v", live.KPI)
	}
	if live.Self.CPUm != 3 || live.Self.MemBytes != 30<<20 {
		t.Fatalf("self: %+v", live.Self)
	}
	if len(live.Components) != 1 || live.Components[0].ReadyPods != 1 || live.Components[0].ReqCPUm != 250 {
		t.Fatalf("components: %+v", live.Components)
	}
	if len(live.Nodes) != 1 || live.Nodes[0].CPUShare != 2 {
		t.Fatalf("nodes: %+v", live.Nodes)
	}
	if len(live.Checks) == 0 {
		t.Fatal("live checks missing")
	}
}

func TestSeriesAndReport(t *testing.T) {
	srv, st, fs, _ := newTestServer(t)
	seedLive(t, st, fs)
	var ser SeriesResponse
	get(t, srv, "/api/series?from=1700000100&to=1700000700", &ser)
	if ser.Step != 15 || len(ser.Components["daemonset-sensor"]) != 40 || len(ser.Cluster) != 40 {
		t.Fatalf("series: step %d, %d pts", ser.Step, len(ser.Components["daemonset-sensor"]))
	}
	if _, ok := ser.Components["self"]; ok {
		t.Fatal("self must not appear in Falcon series")
	}
	var b report.Bundle
	get(t, srv, "/api/report?from=1700000100&to=1700000700", &b)
	if !strings.Contains(b.Report.Headline, "Over 10 minutes on 1 node,") {
		t.Fatalf("report headline: %q", b.Report.Headline)
	}
	if rec := get(t, srv, "/api/report?from=5&to=1", nil); rec.Code != 400 {
		t.Fatalf("from>=to should 400, got %d", rec.Code)
	}
}

func post(srv http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	return rec
}

func TestAssessmentsFlow(t *testing.T) {
	srv, st, fs, now := newTestServer(t)
	seedLive(t, st, fs)
	// a cross-site form post (no JSON content type) is refused
	req := httptest.NewRequest("POST", "/api/assessments", strings.NewReader(`{"duration":"5m"}`))
	req.Header.Set("Content-Type", "text/plain")
	if rec := httptest.NewRecorder(); func() int { srv.ServeHTTP(rec, req); return rec.Code }() != 415 {
		t.Fatal("non-JSON POST must be rejected")
	}
	if rec := post(srv, "/api/assessments", `{"duration":"7m"}`); rec.Code != 400 {
		t.Fatalf("bad duration: %d", rec.Code)
	}
	rec := post(srv, "/api/assessments", `{"duration":"5m"}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var a store.Assessment
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec := post(srv, "/api/assessments", `{"duration":"5m"}`); rec.Code != 409 {
		t.Fatalf("second scan should 409, got %d", rec.Code)
	}
	var d AssessmentDetail
	get(t, srv, "/api/assessments/"+itoa(a.ID), &d)
	if d.Assessment.Status != store.StatusRunning || d.Bundle != nil {
		t.Fatalf("running detail: %+v", d)
	}
	if rec := get(t, srv, "/api/assessments/"+itoa(a.ID)+"/export", nil); rec.Code != 409 {
		t.Fatalf("export of running should 409, got %d", rec.Code)
	}
	*now = now.Add(5 * time.Minute)
	get(t, srv, "/api/assessments/"+itoa(a.ID), &d)
	if d.Assessment.Status != store.StatusComplete || d.Bundle == nil {
		t.Fatalf("complete detail: %+v", d.Assessment)
	}
	exp := get(t, srv, "/api/assessments/"+itoa(a.ID)+"/export", nil)
	if exp.Code != 200 || !strings.HasPrefix(exp.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(exp.Header().Get("Content-Disposition"), "falcon-resource-test-kind-test-") {
		t.Fatalf("export: %d %v", exp.Code, exp.Header())
	}
	if rec := post(srv, "/api/assessments", `{"lookback":"1h"}`); rec.Code != 201 {
		t.Fatalf("history report: %d %s", rec.Code, rec.Body)
	}
	var list []store.Assessment
	get(t, srv, "/api/assessments", &list)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
	if rec := get(t, srv, "/api/assessments/999", nil); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestProbesAndStatic(t *testing.T) {
	srv, _, fs, _ := newTestServer(t)
	if rec := get(t, srv, "/readyz", nil); rec.Code != 503 {
		t.Fatalf("not ready before first tick: %d", rec.Code)
	}
	fs.s.LastTick = t0
	if rec := get(t, srv, "/readyz", nil); rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	if rec := get(t, srv, "/healthz", nil); rec.Code != 200 {
		t.Fatal("healthz")
	}
	var su setup.Result
	get(t, srv, "/api/setup", &su)
	if !su.OK {
		t.Fatal("setup passthrough")
	}

	// SPA fallback: unknown non-API path serves index.html
	rec := get(t, srv, "/assessments/3", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("spa fallback: %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("CSP header missing")
	}
	// unknown API path is a real 404, not index.html
	if rec := get(t, srv, "/api/nope", nil); rec.Code != 404 {
		t.Fatalf("api 404: %d", rec.Code)
	}
	// pre-gzipped asset
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(body, []byte("gz")) ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("gzip asset: %v %q", rec.Header(), body)
	}
}
```

Add these two helpers to the bottom of the test file:

```go
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func discoverResult() discover.Result {
	return discover.Result{
		Workloads:  []discover.Workload{{Component: model.DaemonSetSensor, Namespace: "falcon-system", Kind: "DaemonSet", Name: "falcon-sensor"}},
		Namespaces: []string{"falcon-system"},
	}
}
```

They need `"strconv"` and the `internal/discover` package added to the test imports.

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/web/`
Expected: FAIL (`undefined: New`)

- [ ] **Step 3: Implement `live.go`**

```go
package web

import (
	"sort"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/analyze"
	"github.com/vianneyp72/falcon-resource-test/internal/collect"
	"github.com/vianneyp72/falcon-resource-test/internal/discover"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

// Live is the landing page payload. "Now" values come from the latest tick;
// checks cover the last hour.
type Live struct {
	Cluster          string            `json:"cluster"`
	Version          string            `json:"version"`
	GeneratedAt      time.Time         `json:"generated_at"`
	LastTick         time.Time         `json:"last_tick"`
	IntervalSeconds  int64             `json:"interval_seconds"`
	MetricsOK        bool              `json:"metrics_ok"`
	MetricsErr       string            `json:"metrics_error,omitempty"`
	FalconFound      bool              `json:"falcon_found"`
	Discovery        discover.Result   `json:"discovery"`
	Persistent       bool              `json:"persistent"`
	RetentionSeconds int64             `json:"retention_seconds"`
	FalconAPI        string            `json:"falcon_api"` // off | ok | the last error
	KPI              KPI               `json:"kpi"`
	Self             Usage             `json:"self"`
	Components       []LiveComponent   `json:"components"`
	Nodes            []LiveNode        `json:"nodes"`
	Checks           []analyze.Check   `json:"checks"`
	Running          *store.Assessment `json:"running,omitempty"`
	Disk             Disk              `json:"disk"`
}

type KPI struct {
	CPUm        int64   `json:"cpu_m"`
	CPUPct      float64 `json:"cpu_pct"` // of cluster allocatable
	MemBytes    int64   `json:"mem_bytes"`
	MemPct      float64 `json:"mem_pct"`
	Restarts24h int     `json:"restarts_24h"`
	Ready       int32   `json:"ready"`   // DaemonSet Sensor pods ready
	Desired     int32   `json:"desired"` // DaemonSet Sensor pods desired
	Nodes       int     `json:"nodes"`
}

type Usage struct {
	CPUm     int64 `json:"cpu_m"`
	MemBytes int64 `json:"mem_bytes"`
}

type LiveComponent struct {
	Component model.Component `json:"component"`
	Label     string          `json:"label"`
	Version   string          `json:"version"`
	Pods      int             `json:"pods"`
	ReadyPods int             `json:"ready_pods"`
	CPUm      int64           `json:"cpu_m"`
	MemBytes  int64           `json:"mem_bytes"`
	ReqCPUm   int64           `json:"req_cpu_m"` // per pod
	ReqMem    int64           `json:"req_mem"`
	LimCPUm   int64           `json:"lim_cpu_m"` // per pod, 0 = none
	LimMem    int64           `json:"lim_mem"`
	Status    string          `json:"status"` // ok | degraded | not_running
}

type LiveNode struct {
	Node     string  `json:"node"`
	CPUm     int64   `json:"cpu_m"`
	MemBytes int64   `json:"mem_bytes"`
	CPUShare float64 `json:"cpu_share"` // % of node allocatable
	MemShare float64 `json:"mem_share"`
}

type Disk struct {
	UsedBytes  int64 `json:"used_bytes"`
	LimitBytes int64 `json:"limit_bytes"` // 0 = unknown
	Near       bool  `json:"near"`        // over 80%; retention is being tightened
}

func (s *Server) buildLive(now time.Time) Live {
	st := s.d.Collector.Status()
	l := Live{
		Cluster: s.d.Cluster, Version: s.d.Version, GeneratedAt: now, LastTick: st.LastTick,
		IntervalSeconds: int64(s.d.Interval / time.Second),
		MetricsOK: st.MetricsOK, MetricsErr: st.MetricsErr,
		FalconFound: len(st.Discovery.Workloads) > 0, Discovery: st.Discovery,
		Persistent: s.d.Persistent, RetentionSeconds: int64(s.d.Retention / time.Second),
		FalconAPI: "off",
	}
	if s.d.Sensors != nil {
		l.FalconAPI = "ok"
		if e := s.d.Sensors.Err(); e != "" {
			l.FalconAPI = e
		}
	}

	comps := map[model.Component]*LiveComponent{}
	comp := func(c model.Component) *LiveComponent {
		if comps[c] == nil {
			comps[c] = &LiveComponent{Component: c, Label: c.Label()}
		}
		return comps[c]
	}
	nodeUse := map[string]*LiveNode{}
	for _, x := range st.Latest {
		if x.Component == model.Self {
			l.Self.CPUm += x.CPUm
			l.Self.MemBytes += x.MemBytes
			continue
		}
		l.KPI.CPUm += x.CPUm
		l.KPI.MemBytes += x.MemBytes
		c := comp(x.Component)
		c.CPUm += x.CPUm
		c.MemBytes += x.MemBytes
		if nodeUse[x.Node] == nil {
			nodeUse[x.Node] = &LiveNode{Node: x.Node}
		}
		nodeUse[x.Node].CPUm += x.CPUm
		nodeUse[x.Node].MemBytes += x.MemBytes
	}

	var allocCPU, allocMem int64
	alloc := map[string]model.NodeSample{}
	for _, n := range st.LatestNodes {
		allocCPU += n.AllocCPUm
		allocMem += n.AllocMem
		alloc[n.Node] = n
	}
	l.KPI.Nodes = len(st.LatestNodes)
	l.KPI.CPUPct = analyze.Pct(l.KPI.CPUm, allocCPU)
	l.KPI.MemPct = analyze.Pct(l.KPI.MemBytes, allocMem)

	for _, p := range st.Pods {
		c := comp(p.Component)
		c.Pods++
		if p.Ready {
			c.ReadyPods++
		}
	}
	var specs []model.ContainerSpec
	if sn, ok, _ := s.d.Store.LatestSnapshot(model.SnapPodSpecs); ok {
		sn.Decode(&specs)
	}
	for _, sp := range specs {
		c := comps[sp.Component]
		if c == nil {
			continue
		}
		c.ReqCPUm += sp.ReqCPUm
		c.ReqMem += sp.ReqMem
		c.LimCPUm += sp.LimCPUm
		c.LimMem += sp.LimMem
		if c.Version == "" {
			c.Version = sp.Version
		}
	}
	for _, id := range model.FalconComponents {
		c := comps[id]
		if c == nil {
			continue
		}
		switch {
		case c.Pods == 0:
			c.Status = "not_running"
		case c.ReadyPods < c.Pods:
			c.Status = "degraded"
		default:
			c.Status = "ok"
		}
		l.Components = append(l.Components, *c)
	}

	for name, n := range nodeUse {
		if a, ok := alloc[name]; ok {
			n.CPUShare = analyze.Pct(n.CPUm, a.AllocCPUm)
			n.MemShare = analyze.Pct(n.MemBytes, a.AllocMem)
		}
		l.Nodes = append(l.Nodes, *n)
	}
	sort.Slice(l.Nodes, func(i, j int) bool {
		if l.Nodes[i].CPUShare != l.Nodes[j].CPUShare {
			return l.Nodes[i].CPUShare > l.Nodes[j].CPUShare
		}
		return l.Nodes[i].Node < l.Nodes[j].Node
	})

	if sn, ok, _ := s.d.Store.LatestSnapshot(model.SnapDaemonSets); ok {
		var ds []model.DaemonSetState
		sn.Decode(&ds)
		for _, d := range ds {
			l.KPI.Ready += d.Ready
			l.KPI.Desired += d.Desired
		}
	}
	if evs, err := s.d.Store.Events(now.Add(-24*time.Hour), now.Add(time.Second)); err == nil {
		for _, e := range evs {
			if e.Falcon && e.Kind == model.EventRestart {
				l.KPI.Restarts24h++
			}
		}
	}

	l.Checks = s.liveChecks(now, st)

	s.d.Assess.Refresh()
	if run, ok, _ := s.d.Store.RunningAssessment(); ok {
		l.Running = &run
	}
	if used, err := s.d.Store.SizeBytes(); err == nil {
		l.Disk = Disk{UsedBytes: used, LimitBytes: s.d.DiskLimit, Near: s.d.DiskLimit > 0 && used > s.d.DiskLimit*8/10}
	}
	return l
}

// liveChecks runs the health checks over the last hour, cached for a minute:
// they change slowly and are the most expensive part of /api/live.
func (s *Server) liveChecks(now time.Time, st collect.Status) []analyze.Check {
	return s.checks.get(now, time.Minute, func() []analyze.Check {
		in, err := analyze.Load(s.d.Store, now.Add(-time.Hour), now, s.d.Interval, st.KACPresent, s.sensorsKnown())
		if err != nil {
			return nil
		}
		checks := analyze.Analyze(in).Checks
		if s.d.Sensors != nil && !s.d.Sensors.Known() && s.d.Sensors.Err() != "" {
			for i := range checks {
				if checks[i].ID == "sensor" {
					checks[i].Summary = "Not measured: " + s.d.Sensors.Err()
				}
			}
		}
		return checks
	})
}
```

- [ ] **Step 4: Implement `server.go`**

```go
// Package web serves the JSON API, the HTML export, and the embedded UI.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/analyze"
	"github.com/vianneyp72/falcon-resource-test/internal/assess"
	"github.com/vianneyp72/falcon-resource-test/internal/collect"
	"github.com/vianneyp72/falcon-resource-test/internal/model"
	"github.com/vianneyp72/falcon-resource-test/internal/report"
	"github.com/vianneyp72/falcon-resource-test/internal/setup"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
)

type StatusSource interface{ Status() collect.Status }

type Deps struct {
	Store        *store.Store
	Collector    StatusSource
	Assess       *assess.Manager
	Setup        func(context.Context) setup.Result
	Static       fs.FS
	Cluster      string
	Version      string
	Interval     time.Duration
	Retention    time.Duration
	Persistent   bool
	DiskLimit    int64
	Sensors      SensorStatus // nil when the Falcon API integration is off (Task 17)
	Now          func() time.Time
}

// SensorStatus is the Falcon API poller's state.
type SensorStatus interface {
	Known() bool  // last poll succeeded
	Err() string // last poll error, "" if none
}

type Server struct {
	d      Deps
	h      http.Handler
	live   cached[Live]
	checks cached[[]analyze.Check]
	setup  cached[setup.Result]
}

// cached memoizes one value for a TTL. Concurrent callers wait for the
// first computation instead of all running it.
type cached[T any] struct {
	mu sync.Mutex
	at time.Time
	v  T
	ok bool
}

func (c *cached[T]) get(now time.Time, ttl time.Duration, fn func() T) T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ok || now.Sub(c.at) >= ttl {
		c.v, c.at, c.ok = fn(), now, true
	}
	return c.v
}

func (c *cached[T]) reset() {
	c.mu.Lock()
	c.ok = false
	c.mu.Unlock()
}

type SeriesResponse struct {
	Step       int64                     `json:"step"` // seconds
	Components map[string][]store.Point  `json:"components"`
	Cluster    []store.ClusterPoint      `json:"cluster,omitempty"`
}

type AssessmentDetail struct {
	Assessment store.Assessment `json:"assessment"`
	Bundle     json.RawMessage  `json:"bundle,omitempty"` // report.Bundle once complete
}

func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Server{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/live", s.handleLive)
	mux.HandleFunc("GET /api/series", s.handleSeries)
	mux.HandleFunc("GET /api/report", s.handleReport)
	mux.HandleFunc("GET /api/assessments", s.handleList)
	mux.HandleFunc("POST /api/assessments", s.handleCreate)
	mux.HandleFunc("GET /api/assessments/{id}", s.handleDetail)
	mux.HandleFunc("GET /api/assessments/{id}/export", s.handleExport)
	mux.HandleFunc("GET /api/setup", s.handleSetup)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("/", staticHandler(d.Static))
	s.h = secure(mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func (s *Server) sensorsKnown() bool { return s.d.Sensors != nil && s.d.Sensors.Known() }

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	now := s.d.Now()
	writeJSON(w, 200, s.live.get(now, 10*time.Second, func() Live { return s.buildLive(now) }))
}

var errRange = errors.New("from and to must be unix seconds with from < to, at most 31 days apart")

func (s *Server) window(r *http.Request, def time.Duration) (time.Time, time.Time, error) {
	now := s.d.Now()
	from, to := now.Add(-def), now
	q := r.URL.Query()
	for key, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return from, to, errRange
			}
			*dst = time.Unix(n, 0)
		}
	}
	if !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		return from, to, errRange
	}
	return from, to, nil
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.window(r, time.Hour)
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	node, only := r.URL.Query().Get("node"), r.URL.Query().Get("component")
	step := store.StepFor(to.Sub(from))
	series, err := s.d.Store.ComponentSeries(from, to, step, node)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	resp := SeriesResponse{Step: int64(step / time.Second), Components: map[string][]store.Point{}}
	for c, pts := range series {
		if c == model.Self || (only != "" && string(c) != only) {
			continue
		}
		resp.Components[string(c)] = pts
	}
	if node == "" {
		if resp.Cluster, err = s.d.Store.ClusterSeries(from, to, step); err != nil {
			httpError(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.window(r, time.Hour)
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	b, err := report.Build(s.d.Store, from, to, s.d.Interval, s.d.Collector.Status().KACPresent, s.sensorsKnown())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	s.d.Assess.Refresh()
	list, err := s.d.Store.ListAssessments()
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []store.Assessment{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	// Requiring a JSON content type forces a CORS preflight for any
	// cross-site request, so a malicious page can't start scans.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpError(w, 415, "Content-Type must be application/json")
		return
	}
	var body struct {
		Duration string `json:"duration"` // timed scan: 5m | 15m | 1h | 24h
		Lookback string `json:"lookback"` // instant report over existing history
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		httpError(w, 400, "invalid JSON body")
		return
	}
	var a store.Assessment
	var err error
	switch {
	case body.Duration != "":
		d, perr := time.ParseDuration(body.Duration)
		if perr != nil {
			httpError(w, 400, assess.ErrDuration.Error())
			return
		}
		a, err = s.d.Assess.Start(d)
	case body.Lookback != "":
		d, perr := time.ParseDuration(body.Lookback)
		if perr != nil {
			httpError(w, 400, assess.ErrLookback.Error())
			return
		}
		a, err = s.d.Assess.History(d)
	default:
		httpError(w, 400, "duration or lookback is required")
		return
	}
	switch {
	case errors.Is(err, assess.ErrRunning):
		httpError(w, 409, err.Error())
	case errors.Is(err, assess.ErrDuration), errors.Is(err, assess.ErrLookback):
		httpError(w, 400, err.Error())
	case err != nil:
		httpError(w, 500, err.Error())
	default:
		s.live.reset() // show the running banner immediately
		writeJSON(w, 201, a)
	}
}

func (s *Server) assessment(w http.ResponseWriter, r *http.Request) (store.Assessment, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, 404, "assessment not found")
		return store.Assessment{}, false
	}
	s.d.Assess.Refresh()
	a, ok, err := s.d.Store.GetAssessment(id)
	if err != nil {
		httpError(w, 500, err.Error())
		return a, false
	}
	if !ok {
		httpError(w, 404, "assessment not found")
	}
	return a, ok
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assessment(w, r)
	if !ok {
		return
	}
	d := AssessmentDetail{Assessment: a}
	if res, ok, _ := s.d.Store.AssessmentResult(a.ID); ok {
		d.Bundle = res
	}
	writeJSON(w, 200, d)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assessment(w, r)
	if !ok {
		return
	}
	res, ok, _ := s.d.Store.AssessmentResult(a.ID)
	if a.Status != store.StatusComplete || !ok {
		httpError(w, 409, "report is not ready: assessment is "+a.Status)
		return
	}
	var b report.Bundle
	if err := json.Unmarshal(res, &b); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	var buf bytes.Buffer
	if err := report.Render(&buf, report.Data{Cluster: s.d.Cluster, Bundle: b, Generated: s.d.Now(), Version: s.d.Version}); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	name := fmt.Sprintf("falcon-resource-test-%s-%s.html", safeName(s.d.Cluster), a.EndsAt.UTC().Format("20060102-1504"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(buf.Bytes())
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.setup.get(s.d.Now(), 30*time.Second, func() setup.Result { return s.d.Setup(r.Context()) }))
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.d.Collector.Status().LastTick.IsZero() {
		http.Error(w, "waiting for first collection", 503)
		return
	}
	io.WriteString(w, "ok")
}

func safeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			b[i] = '-'
		}
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// staticHandler serves the built UI. Unknown paths get index.html so client
// routes like /assessments/3 work on reload. Vite emits .gz siblings at
// build time; they're served as-is to gzip-capable clients.
func staticHandler(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpError(w, 404, "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpError(w, 405, "method not allowed")
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if fi, err := fs.Stat(root, name); name == "" || err != nil || fi.IsDir() {
			name = "index.html"
		}
		h := w.Header()
		ctype := mime.TypeByExtension(path.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		h.Set("Content-Type", ctype)
		h.Set("Vary", "Accept-Encoding")
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable") // hashed filenames
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			if f, err := root.Open(name + ".gz"); err == nil {
				defer f.Close()
				h.Set("Content-Encoding", "gzip")
				io.Copy(w, f)
				return
			}
		}
		f, err := root.Open(name)
		if err != nil {
			h.Del("Cache-Control")
			http.Error(w, "UI not built. Run `make ui` before `go build`.", 503)
			return
		}
		defer f.Close()
		io.Copy(w, f)
	})
}
```

- [ ] **Step 5: Implement `embed.go`**

```go
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets is the built UI (Vite output, see ui/vite.config.ts). Until
// `make ui` runs, dist holds only .gitkeep and every page returns 503.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
```

```bash
mkdir -p internal/web/dist && touch internal/web/dist/.gitkeep
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -w internal/web && go vet ./internal/web/ && go test ./internal/web/`
Expected: `ok`. If `TestLive` fails on `CPUPct`, check that `Latest` in the fixture holds exactly one tick: the last two samples, which are the sensor and the tool itself.

- [ ] **Step 7: Commit**

```bash
git add internal/web
git commit -m "falcon-resource-test: add HTTP API and static UI serving

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 16: main wiring

All configuration comes from environment variables, which the Helm chart sets (Task 24), so there's no flag parsing. Parsing lives in `config.go` so it can be tested without a cluster.

**Files:**
- Create: `cmd/falcon-resource-test/config.go`
- Create: `cmd/falcon-resource-test/main.go`
- Test: `cmd/falcon-resource-test/config_test.go`

| Env var | Default | Set by the chart from |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | fixed |
| `DATA_DIR` | `/data` | fixed (the volume mount) |
| `CLUSTER_NAME` | `kubernetes` | `clusterName` |
| `COLLECTION_INTERVAL` | `15s` | `collection.interval` |
| `DISCOVERY_NAMESPACES` | empty = auto | `discovery.namespaces` (comma-joined) |
| `PERSISTENT` | `false` | `persistence.enabled` |
| `RETENTION_DAYS` | `30` | `persistence.retentionDays` (ignored unless `PERSISTENT`) |
| `DISK_LIMIT_BYTES` | `0` = unknown | emptyDir `sizeLimit` or PVC size |
| `EXTRA_SCAN_DURATIONS` | empty | e2e only, for example `1m` |
| `POD_NAMESPACE`, `POD_NAME` | empty | Downward API `metadata.*` |
| `MEMORY_LIMIT_BYTES` | `0` | Downward API `limits.memory` |

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.Interval != 15*time.Second || c.Cluster != "kubernetes" {
		t.Fatalf("defaults: %+v", c)
	}
	// without persistence: 24h raw, 24h rollups
	if c.RawRetention != 24*time.Hour || c.Retention != 24*time.Hour || len(c.Namespaces) != 0 {
		t.Fatalf("ephemeral retention: %+v", c)
	}
}

func TestLoadConfigPersistent(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"PERSISTENT": "true", "RETENTION_DAYS": "7", "DISCOVERY_NAMESPACES": "falcon-system, falcon-kac",
		"EXTRA_SCAN_DURATIONS": "1m", "MEMORY_LIMIT_BYTES": "134217728",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RawRetention != 48*time.Hour || c.Retention != 7*24*time.Hour {
		t.Fatalf("persistent retention: %+v", c)
	}
	if len(c.Namespaces) != 2 || c.Namespaces[1] != "falcon-kac" {
		t.Fatalf("namespaces: %q", c.Namespaces)
	}
	if len(c.ExtraScans) != 1 || c.ExtraScans[0] != time.Minute || c.MemLimit != 128<<20 {
		t.Fatalf("extras: %+v", c)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	for _, m := range []map[string]string{
		{"COLLECTION_INTERVAL": "2s"}, // floor is 5s; faster just burns API server CPU
		{"COLLECTION_INTERVAL": "soon"},
		{"PERSISTENT": "true", "RETENTION_DAYS": "0"},
		{"MEMORY_LIMIT_BYTES": "lots"},
	} {
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("%v: want error", m)
		}
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./cmd/falcon-resource-test/`
Expected: FAIL (`undefined: loadConfig`)

- [ ] **Step 3: Implement `config.go`**

```go
package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Listen, DataDir, Cluster string
	Interval                 time.Duration
	Namespaces               []string
	Persistent               bool
	RawRetention, Retention  time.Duration
	DiskLimit                int64
	ExtraScans               []time.Duration
	SelfNamespace, SelfPod   string
	MemLimit                 int64
}

func loadConfig(getenv func(string) string) (config, error) {
	str := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	c := config{
		Listen: str("LISTEN_ADDR", ":8080"), DataDir: str("DATA_DIR", "/data"),
		Cluster: str("CLUSTER_NAME", "kubernetes"),
		SelfNamespace: getenv("POD_NAMESPACE"), SelfPod: getenv("POD_NAME"),
	}
	var err error
	if c.Interval, err = time.ParseDuration(str("COLLECTION_INTERVAL", "15s")); err != nil || c.Interval < 5*time.Second {
		return c, fmt.Errorf("COLLECTION_INTERVAL must be a duration of at least 5s, got %q", getenv("COLLECTION_INTERVAL"))
	}
	for _, ns := range strings.Split(getenv("DISCOVERY_NAMESPACES"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			c.Namespaces = append(c.Namespaces, ns)
		}
	}
	c.Persistent = str("PERSISTENT", "false") == "true"
	c.RawRetention, c.Retention = 24*time.Hour, 24*time.Hour
	if c.Persistent {
		days, err := strconv.Atoi(str("RETENTION_DAYS", "30"))
		if err != nil || days < 1 {
			return c, fmt.Errorf("RETENTION_DAYS must be a whole number of days, at least 1")
		}
		c.RawRetention, c.Retention = 48*time.Hour, time.Duration(days)*24*time.Hour
	}
	for k, dst := range map[string]*int64{"DISK_LIMIT_BYTES": &c.DiskLimit, "MEMORY_LIMIT_BYTES": &c.MemLimit} {
		if *dst, err = strconv.ParseInt(str(k, "0"), 10, 64); err != nil {
			return c, fmt.Errorf("%s must be a byte count, got %q", k, getenv(k))
		}
	}
	for _, d := range strings.Split(getenv("EXTRA_SCAN_DURATIONS"), ",") {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		v, err := time.ParseDuration(d)
		if err != nil {
			return c, fmt.Errorf("EXTRA_SCAN_DURATIONS: %w", err)
		}
		c.ExtraScans = append(c.ExtraScans, v)
	}
	return c, nil
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./cmd/falcon-resource-test/`
Expected: `ok` (the test builds without `main.go`, since `config.go` is all it needs).

- [ ] **Step 5: Implement `main.go`**

```go
// Command falcon-resource-test measures what CrowdStrike Falcon costs a Kubernetes
// cluster. It is read-only: see internal/setup.Required for every call it makes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"k8s.io/client-go/rest"

	"github.com/vianneyp72/falcon-resource-test/internal/assess"
	"github.com/vianneyp72/falcon-resource-test/internal/collect"
	"github.com/vianneyp72/falcon-resource-test/internal/report"
	"github.com/vianneyp72/falcon-resource-test/internal/setup"
	"github.com/vianneyp72/falcon-resource-test/internal/store"
	"github.com/vianneyp72/falcon-resource-test/internal/web"
)

var version = "dev" // set by -ldflags "-X main.version=..."

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	// Leave 10% headroom under the container limit so the GC works
	// harder before the kernel OOM-kills us. An explicit GOMEMLIMIT wins.
	if cfg.MemLimit > 0 && os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(cfg.MemLimit * 9 / 10)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	rc, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	rc.UserAgent = "falcon-resource-test/" + version
	rc.QPS, rc.Burst = 5, 10 // well below defaults; we make a handful of calls per tick
	kube, err := collect.NewKube(rc)
	if err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "frt.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	col := collect.New(kube, st, collect.Config{
		Interval: cfg.Interval, SelfNamespace: cfg.SelfNamespace, SelfPod: cfg.SelfPod,
		Namespaces: cfg.Namespaces, RawRetention: cfg.RawRetention, Retention: cfg.Retention,
	})

	build := func(from, to time.Time) ([]byte, error) {
		b, err := report.Build(st, from, to, cfg.Interval, col.Status().KACPresent, false)
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	}
	am := assess.New(st, time.Now, build)
	for _, d := range cfg.ExtraScans {
		am.Allow(d)
	}
	if err := am.Recover(); err != nil {
		slog.Warn("recover assessments", "err", err)
	}

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Deps{
			Store: st, Collector: col, Assess: am, Static: web.Assets(),
			Setup: func(ctx context.Context) setup.Result {
				return setup.Run(ctx, kube.Authz, kube.Metrics, len(col.Status().Discovery.Workloads))
			},
			Cluster: cfg.Cluster, Version: version, Interval: cfg.Interval,
			Retention: cfg.Retention, Persistent: cfg.Persistent, DiskLimit: cfg.DiskLimit,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go loops(ctx, cfg, col, am, st)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("falcon-resource-test started", "version", version, "addr", cfg.Listen, "cluster", cfg.Cluster, "persistent", cfg.Persistent)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// loops runs all background work on one goroutine, so ticks never overlap
// and the store sees one writer.
func loops(ctx context.Context, cfg config, col *collect.Collector, am *assess.Manager, st *store.Store) {
	// Discover before the first fast tick so it knows where Falcon lives.
	col.SlowTick(ctx)
	col.FastTick(ctx)

	fast := time.NewTicker(cfg.Interval)
	slow := time.NewTicker(time.Minute)
	refresh := time.NewTicker(15 * time.Second)
	defer fast.Stop()
	defer slow.Stop()
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			if err := col.FastTick(ctx); err != nil {
				slog.Warn("fast tick", "err", err)
			}
		case <-slow.C:
			col.SlowTick(ctx)
			guardDisk(st, cfg, time.Now())
		case <-refresh.C:
			am.Refresh()
		}
	}
}

// guardDisk keeps the database under 80% of the volume. When it's over,
// it drops the oldest half of the retention window. /api/live reports the
// same 80% threshold, so the UI shows a banner while this is happening.
func guardDisk(st *store.Store, cfg config, now time.Time) {
	if cfg.DiskLimit <= 0 {
		return
	}
	used, err := st.SizeBytes()
	if err != nil || used <= cfg.DiskLimit*8/10 {
		return
	}
	cutoff := now.Add(-cfg.Retention / 2)
	slog.Warn("data volume above 80%, tightening retention", "used_bytes", used, "limit_bytes", cfg.DiskLimit, "expire_before", cutoff)
	if err := st.Expire(cutoff); err != nil {
		slog.Warn("expire", "err", err)
	}
}
```

`Sensors` stays nil here, and `report.Build` gets `false`, because the Falcon API integration isn't built yet. Task 17 wires both up.

- [ ] **Step 6: Build and vet everything**

Run: `gofmt -l . ; go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=test" -o bin/falcon-resource-test ./cmd/falcon-resource-test && ls -la bin/falcon-resource-test`
Expected: `gofmt -l` prints nothing, every package reports `ok`, and the binary is under 30 MB uncompressed (the image gate in Task 26 measures it compressed).

> **Note:** `go build` succeeds even before the UI exists, because `dist/.gitkeep` satisfies `//go:embed all:dist`. Pages return 503 until `make ui` runs (Task 22).

- [ ] **Step 7: Commit**

```bash
git add cmd/falcon-resource-test
git commit -m "falcon-resource-test: wire collector, store, assessments, and HTTP server

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
