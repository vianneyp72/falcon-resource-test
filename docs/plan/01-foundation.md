# Part 1: Foundation (Tasks 0–4)

All commands run from `` unless noted.

---

### Task 0: Toolchain and Go module

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `.gitignore`

- [ ] **Step 1: Install toolchains (macOS dev box; CI installs its own)**

```bash
brew install go kind
go version        # expect go1.24 or newer
kind version
```

Docker Desktop must be running for Tasks 23 and 26. Check with `docker info`.

- [ ] **Step 2: Create the module and pull dependencies**

```bash
mkdir -p falcon-resource-test && cd falcon-resource-test
go mod init github.com/vianneyp72/falcon-resource-test
go get k8s.io/api@latest k8s.io/apimachinery@latest k8s.io/client-go@latest k8s.io/metrics@latest modernc.org/sqlite@latest
```

- [ ] **Step 3: Makefile**

```makefile
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test ui build image e2e
test:
	go test ./...
ui:
	cd ui && npm ci && npm run build
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/falcon-resource-test ./cmd/falcon-resource-test
image:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t falcon-resource-test:dev .
e2e:
	./test/e2e.sh
```

- [ ] **Step 4: .gitignore**

```
bin/
ui/node_modules/
internal/web/dist/*
!internal/web/dist/.gitkeep
```

- [ ] **Step 5: Commit**

```bash
git add falcon-resource-test
git commit -m "falcon-resource-test: init Go module

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 1: Shared model types

**Files:**
- Create: `internal/model/model.go`
- Test: `internal/model/model_test.go`

- [ ] **Step 1: Write the failing test**

```go
package model

import "testing"

func TestComponentLabel(t *testing.T) {
	cases := map[Component]string{
		DaemonSetSensor: "DaemonSet Sensor",
		KAC:             "KAC",
		IAR:             "IAR",
		Injector:        "Container Sensor Injector",
		Operator:        "Falcon Operator",
		Self:            "Falcon Resource Test",
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
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/model/`
Expected: FAIL (`undefined: Component`)

- [ ] **Step 3: Implement**

```go
// Package model holds the types shared by every falcon-resource-test package.
// CPU is always millicores, memory always bytes.
package model

import "time"

type Component string

const (
	DaemonSetSensor Component = "daemonset-sensor"
	KAC             Component = "kac"
	IAR             Component = "iar"
	Injector        Component = "injector"
	Operator        Component = "operator"
	Self            Component = "self" // falcon-resource-test itself, reported separately
)

// FalconComponents is the display order for Falcon (non-Self) components.
var FalconComponents = []Component{DaemonSetSensor, KAC, IAR, Injector, Operator}

var labels = map[Component]string{
	DaemonSetSensor: "DaemonSet Sensor",
	KAC:             "KAC",
	IAR:             "IAR",
	Injector:        "Container Sensor Injector",
	Operator:        "Falcon Operator",
	Self:            "Falcon Resource Test",
}

func (c Component) Label() string {
	if l, ok := labels[c]; ok {
		return l
	}
	return string(c)
}

// Sample is one container's usage at one instant. For 5-minute rollups,
// CPUm/MemBytes are averages and CPUMaxm/MemMax the bucket maxima; for raw
// samples the max fields equal the value.
type Sample struct {
	TS                         time.Time
	Node, Namespace, Pod       string
	Container                  string
	Component                  Component
	CPUm, MemBytes             int64
	CPUMaxm, MemMax            int64
}

type NodeSample struct {
	TS                   time.Time
	Node                 string
	AllocCPUm, AllocMem  int64
	UsedCPUm, UsedMem    int64
}

type EventKind string

const (
	EventRestart     EventKind = "restart"
	EventOOMKill     EventKind = "oomkill"
	EventCrashLoop   EventKind = "crashloop"
	EventEviction    EventKind = "eviction"
	EventPressure    EventKind = "pressure"
	EventReadyChange EventKind = "ready_change"
)

type Event struct {
	TS                         time.Time
	Kind                       EventKind
	Namespace, Object          string
	Node                       string
	Reason, Message            string
	Falcon                     bool // object is a Falcon pod
}

type SnapshotKind string

const (
	SnapDaemonSets SnapshotKind = "daemonsets"  // []DaemonSetState
	SnapWebhooks   SnapshotKind = "webhooks"    // []WebhookState
	SnapPodSpecs   SnapshotKind = "pod_specs"   // []ContainerSpec
	SnapFalconAPI  SnapshotKind = "falcon_api"  // []SensorHost
)

type DaemonSetState struct {
	Namespace, Name          string
	Desired, Ready, Updated  int32
}

type WebhookState struct {
	Name           string
	Kind           string // "mutating" | "validating"
	FailurePolicy  string // "Ignore" | "Fail"
	TimeoutSeconds int32
}

type ContainerSpec struct {
	Component                Component
	Namespace, Pod, Container string
	Node                     string
	Image, Version           string // Version = image tag
	Backend                  string // "kernel" | "bpf" | "" (sensor only)
	PriorityClass            string
	ReqCPUm, ReqMem          int64
	LimCPUm, LimMem          int64 // 0 = no limit
}

type SensorHost struct {
	Hostname string
	Version  string
	Online   bool
	RFM      bool
	LastSeen time.Time
}
```

- [ ] **Step 4: Run gofmt and the tests**

Run: `gofmt -w internal/model && go test ./internal/model/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/model
git commit -m "falcon-resource-test: add shared model types

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Store core (schema, inserts, range queries, snapshots)

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/store/`
Expected: FAIL (`undefined: Open`)

- [ ] **Step 3: Implement**

```go
// Package store persists samples, events, snapshots, and assessments in an
// embedded SQLite file. One connection, small page cache: memory stays flat
// no matter how much history accumulates.
package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

const schema = `
CREATE TABLE IF NOT EXISTS samples (
  ts INTEGER NOT NULL, node TEXT, ns TEXT, pod TEXT, container TEXT, component TEXT,
  cpu_m INTEGER, mem_bytes INTEGER);
CREATE INDEX IF NOT EXISTS samples_ts ON samples(ts);
CREATE TABLE IF NOT EXISTS samples_5m (
  ts INTEGER NOT NULL, node TEXT, ns TEXT, pod TEXT, container TEXT, component TEXT,
  cpu_m INTEGER, mem_bytes INTEGER, cpu_max INTEGER, mem_max INTEGER);
CREATE INDEX IF NOT EXISTS samples_5m_ts ON samples_5m(ts);
CREATE TABLE IF NOT EXISTS node_samples (
  ts INTEGER NOT NULL, node TEXT, alloc_cpu_m INTEGER, alloc_mem INTEGER, used_cpu_m INTEGER, used_mem INTEGER);
CREATE INDEX IF NOT EXISTS node_samples_ts ON node_samples(ts);
CREATE TABLE IF NOT EXISTS node_samples_5m (
  ts INTEGER NOT NULL, node TEXT, alloc_cpu_m INTEGER, alloc_mem INTEGER, used_cpu_m INTEGER, used_mem INTEGER);
CREATE INDEX IF NOT EXISTS node_samples_5m_ts ON node_samples_5m(ts);
CREATE TABLE IF NOT EXISTS events (
  ts INTEGER NOT NULL, kind TEXT, ns TEXT, object TEXT, node TEXT, reason TEXT, message TEXT, falcon INTEGER);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS snapshots (ts INTEGER NOT NULL, kind TEXT NOT NULL, json BLOB);
CREATE INDEX IF NOT EXISTS snapshots_kind_ts ON snapshots(kind, ts);
CREATE TABLE IF NOT EXISTS assessments (
  id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, started_at INTEGER, ends_at INTEGER,
  status TEXT, created_at INTEGER, result BLOB);
`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-4000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) InsertSamples(in []model.Sample) error {
	return s.tx(func(tx *sql.Tx) error {
		st, err := tx.Prepare(`INSERT INTO samples VALUES (?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, x := range in {
			if _, err := st.Exec(x.TS.Unix(), x.Node, x.Namespace, x.Pod, x.Container, string(x.Component), x.CPUm, x.MemBytes); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) InsertNodeSamples(in []model.NodeSample) error {
	return s.tx(func(tx *sql.Tx) error {
		st, err := tx.Prepare(`INSERT INTO node_samples VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, x := range in {
			if _, err := st.Exec(x.TS.Unix(), x.Node, x.AllocCPUm, x.AllocMem, x.UsedCPUm, x.UsedMem); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) InsertEvent(e model.Event) error {
	_, err := s.db.Exec(`INSERT INTO events VALUES (?,?,?,?,?,?,?,?)`,
		e.TS.Unix(), string(e.Kind), e.Namespace, e.Object, e.Node, e.Reason, e.Message, boolInt(e.Falcon))
	return err
}

// Samples returns usage in [from, to). Raw rows are used where they exist;
// 5-minute rollups fill the older part of the range.
func (s *Store) Samples(from, to time.Time) ([]model.Sample, error) {
	rows, err := s.db.Query(`
SELECT ts,node,ns,pod,container,component,cpu_m,mem_bytes,cpu_m,mem_bytes FROM samples WHERE ts>=? AND ts<?
UNION ALL
SELECT ts,node,ns,pod,container,component,cpu_m,mem_bytes,cpu_max,mem_max FROM samples_5m
  WHERE ts>=? AND ts<? AND ts < (SELECT COALESCE(MIN(ts), ?) FROM samples)
ORDER BY 1`, from.Unix(), to.Unix(), from.Unix(), to.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Sample
	for rows.Next() {
		var x model.Sample
		var ts int64
		var comp string
		if err := rows.Scan(&ts, &x.Node, &x.Namespace, &x.Pod, &x.Container, &comp, &x.CPUm, &x.MemBytes, &x.CPUMaxm, &x.MemMax); err != nil {
			return nil, err
		}
		x.TS, x.Component = time.Unix(ts, 0), model.Component(comp)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) NodeSamples(from, to time.Time) ([]model.NodeSample, error) {
	rows, err := s.db.Query(`
SELECT ts,node,alloc_cpu_m,alloc_mem,used_cpu_m,used_mem FROM node_samples WHERE ts>=? AND ts<?
UNION ALL
SELECT ts,node,alloc_cpu_m,alloc_mem,used_cpu_m,used_mem FROM node_samples_5m
  WHERE ts>=? AND ts<? AND ts < (SELECT COALESCE(MIN(ts), ?) FROM node_samples)
ORDER BY 1`, from.Unix(), to.Unix(), from.Unix(), to.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeSample
	for rows.Next() {
		var x model.NodeSample
		var ts int64
		if err := rows.Scan(&ts, &x.Node, &x.AllocCPUm, &x.AllocMem, &x.UsedCPUm, &x.UsedMem); err != nil {
			return nil, err
		}
		x.TS = time.Unix(ts, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) Events(from, to time.Time) ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT ts,kind,ns,object,node,reason,message,falcon FROM events WHERE ts>=? AND ts<? ORDER BY ts`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var ts, falcon int64
		var kind string
		if err := rows.Scan(&ts, &kind, &e.Namespace, &e.Object, &e.Node, &e.Reason, &e.Message, &falcon); err != nil {
			return nil, err
		}
		e.TS, e.Kind, e.Falcon = time.Unix(ts, 0), model.EventKind(kind), falcon == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

type Snapshot struct {
	TS   time.Time
	Kind model.SnapshotKind
	JSON []byte
}

func (s Snapshot) Decode(v any) error { return json.Unmarshal(s.JSON, v) }

// PutSnapshot stores v as JSON, skipping the write when it is identical to the
// latest snapshot of the same kind (so state is stored only on change).
func (s *Store) PutSnapshot(ts time.Time, kind model.SnapshotKind, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if latest, ok, err := s.LatestSnapshot(kind); err != nil {
		return err
	} else if ok && bytes.Equal(latest.JSON, b) {
		return nil
	}
	_, err = s.db.Exec(`INSERT INTO snapshots VALUES (?,?,?)`, ts.Unix(), string(kind), b)
	return err
}

func (s *Store) LatestSnapshot(kind model.SnapshotKind) (Snapshot, bool, error) {
	var snap Snapshot
	var ts int64
	err := s.db.QueryRow(`SELECT ts,json FROM snapshots WHERE kind=? ORDER BY ts DESC, rowid DESC LIMIT 1`, string(kind)).Scan(&ts, &snap.JSON)
	if err == sql.ErrNoRows {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, err
	}
	snap.TS, snap.Kind = time.Unix(ts, 0), kind
	return snap, true, nil
}

// SnapshotsBetween returns the snapshot in effect at `from` (the latest one
// at or before it) followed by every snapshot in (from, to).
func (s *Store) SnapshotsBetween(kind model.SnapshotKind, from, to time.Time) ([]Snapshot, error) {
	rows, err := s.db.Query(`
SELECT ts,json FROM (SELECT ts,json,rowid FROM snapshots WHERE kind=? AND ts<=? ORDER BY ts DESC, rowid DESC LIMIT 1)
UNION ALL
SELECT ts,json FROM snapshots WHERE kind=? AND ts>? AND ts<?
ORDER BY 1`, string(kind), from.Unix(), string(kind), from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		var ts int64
		if err := rows.Scan(&ts, &snap.JSON); err != nil {
			return nil, err
		}
		snap.TS, snap.Kind = time.Unix(ts, 0), kind
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/store && go test ./internal/store/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/store go.mod go.sum
git commit -m "falcon-resource-test: add SQLite store core

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: Chart series, rollup, retention, DB size

**Files:**
- Create: `internal/store/series.go`
- Create: `internal/store/retention.go`
- Test: `internal/store/series_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package store

import (
	"testing"
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
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
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/store/ -run 'Series|Bucketed|Rollup|Expire'`
Expected: FAIL (`s.ComponentSeries undefined`)

- [ ] **Step 3: Implement `series.go`**

```go
package store

import (
	"time"

	"github.com/vianneyp72/falcon-resource-test/internal/model"
)

type Point struct {
	TS       int64 `json:"ts"`
	CPUm     int64 `json:"cpu_m"`
	MemBytes int64 `json:"mem_bytes"`
}

type ClusterPoint struct {
	TS        int64 `json:"ts"`
	AllocCPUm int64 `json:"alloc_cpu_m"`
	AllocMem  int64 `json:"alloc_mem"`
	UsedCPUm  int64 `json:"used_cpu_m"`
	UsedMem   int64 `json:"used_mem"`
}

// unionSamples selects raw rows plus rollup rows older than the oldest raw
// row. Params: ?1 from, ?2 to.
const unionSamples = `
SELECT ts,node,component,cpu_m,mem_bytes FROM samples WHERE ts>=?1 AND ts<?2
UNION ALL
SELECT ts,node,component,cpu_m,mem_bytes FROM samples_5m
  WHERE ts>=?1 AND ts<?2 AND ts<(SELECT COALESCE(MIN(ts),?2) FROM samples)`

const unionNodes = `
SELECT ts,node,alloc_cpu_m,alloc_mem,used_cpu_m,used_mem FROM node_samples WHERE ts>=?1 AND ts<?2
UNION ALL
SELECT ts,node,alloc_cpu_m,alloc_mem,used_cpu_m,used_mem FROM node_samples_5m
  WHERE ts>=?1 AND ts<?2 AND ts<(SELECT COALESCE(MIN(ts),?2) FROM node_samples)`

// ComponentSeries returns per-component totals (summed across pods at each
// tick, then averaged per step bucket). node="" means all nodes.
func (s *Store) ComponentSeries(from, to time.Time, step time.Duration, node string) (map[model.Component][]Point, error) {
	st := int64(step / time.Second)
	rows, err := s.db.Query(`
WITH u AS (`+unionSamples+`),
per_tick AS (SELECT ts, component, SUM(cpu_m) c, SUM(mem_bytes) m FROM u
  WHERE (?4='' OR node=?4) GROUP BY ts, component)
SELECT (ts/?3)*?3 b, component, CAST(AVG(c) AS INTEGER), CAST(AVG(m) AS INTEGER)
FROM per_tick GROUP BY b, component ORDER BY b`, from.Unix(), to.Unix(), st, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.Component][]Point{}
	for rows.Next() {
		var p Point
		var comp string
		if err := rows.Scan(&p.TS, &comp, &p.CPUm, &p.MemBytes); err != nil {
			return nil, err
		}
		out[model.Component(comp)] = append(out[model.Component(comp)], p)
	}
	return out, rows.Err()
}

// ClusterSeries returns whole-cluster allocatable and used, per step bucket.
func (s *Store) ClusterSeries(from, to time.Time, step time.Duration) ([]ClusterPoint, error) {
	st := int64(step / time.Second)
	rows, err := s.db.Query(`
WITH u AS (`+unionNodes+`),
per_tick AS (SELECT ts, SUM(alloc_cpu_m) ac, SUM(alloc_mem) am, SUM(used_cpu_m) uc, SUM(used_mem) um FROM u GROUP BY ts)
SELECT (ts/?3)*?3 b, CAST(AVG(ac) AS INTEGER), CAST(AVG(am) AS INTEGER), CAST(AVG(uc) AS INTEGER), CAST(AVG(um) AS INTEGER)
FROM per_tick GROUP BY b ORDER BY b`, from.Unix(), to.Unix(), st)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterPoint
	for rows.Next() {
		var p ClusterPoint
		if err := rows.Scan(&p.TS, &p.AllocCPUm, &p.AllocMem, &p.UsedCPUm, &p.UsedMem); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BucketedSamples aggregates per container into step buckets (avg + max),
// so analyzing a long window never loads every raw 15s row into memory.
func (s *Store) BucketedSamples(from, to time.Time, step time.Duration) ([]model.Sample, error) {
	st := int64(step / time.Second)
	rows, err := s.db.Query(`
WITH u AS (
  SELECT ts,node,ns,pod,container,component,cpu_m,mem_bytes,cpu_m cx,mem_bytes mx FROM samples WHERE ts>=?1 AND ts<?2
  UNION ALL
  SELECT ts,node,ns,pod,container,component,cpu_m,mem_bytes,cpu_max,mem_max FROM samples_5m
    WHERE ts>=?1 AND ts<?2 AND ts<(SELECT COALESCE(MIN(ts),?2) FROM samples))
SELECT (ts/?3)*?3 b,node,ns,pod,container,component,
  CAST(AVG(cpu_m) AS INTEGER),CAST(AVG(mem_bytes) AS INTEGER),MAX(cx),MAX(mx)
FROM u GROUP BY b,node,ns,pod,container,component ORDER BY b`, from.Unix(), to.Unix(), st)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Sample
	for rows.Next() {
		var x model.Sample
		var ts int64
		var comp string
		if err := rows.Scan(&ts, &x.Node, &x.Namespace, &x.Pod, &x.Container, &comp, &x.CPUm, &x.MemBytes, &x.CPUMaxm, &x.MemMax); err != nil {
			return nil, err
		}
		x.TS, x.Component = time.Unix(ts, 0), model.Component(comp)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) BucketedNodeSamples(from, to time.Time, step time.Duration) ([]model.NodeSample, error) {
	st := int64(step / time.Second)
	rows, err := s.db.Query(`
WITH u AS (`+unionNodes+`)
SELECT (ts/?3)*?3 b,node,CAST(AVG(alloc_cpu_m) AS INTEGER),CAST(AVG(alloc_mem) AS INTEGER),
  CAST(AVG(used_cpu_m) AS INTEGER),CAST(AVG(used_mem) AS INTEGER)
FROM u GROUP BY b,node ORDER BY b`, from.Unix(), to.Unix(), st)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeSample
	for rows.Next() {
		var x model.NodeSample
		var ts int64
		if err := rows.Scan(&ts, &x.Node, &x.AllocCPUm, &x.AllocMem, &x.UsedCPUm, &x.UsedMem); err != nil {
			return nil, err
		}
		x.TS = time.Unix(ts, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

// StepFor picks a chart resolution that keeps any range under ~400 points.
func StepFor(d time.Duration) time.Duration {
	for _, s := range []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour} {
		if d/s <= 400 {
			return s
		}
	}
	return 6 * time.Hour
}
```

- [ ] **Step 4: Implement `retention.go`**

```go
package store

import (
	"database/sql"
	"time"
)

const bucket = 300 // 5 minutes

// Rollup moves raw rows older than cutoff (aligned down to a 5m boundary so
// no bucket is split) into the 5m tables: avg + max per container.
func (s *Store) Rollup(cutoff time.Time) error {
	c := cutoff.Unix() / bucket * bucket
	return s.tx(func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO samples_5m
			 SELECT (ts/300)*300, node, ns, pod, container, component,
			   CAST(AVG(cpu_m) AS INTEGER), CAST(AVG(mem_bytes) AS INTEGER), MAX(cpu_m), MAX(mem_bytes)
			 FROM samples WHERE ts<? GROUP BY 1,2,3,4,5,6`,
			`DELETE FROM samples WHERE ts<?`,
			`INSERT INTO node_samples_5m
			 SELECT (ts/300)*300, node, CAST(AVG(alloc_cpu_m) AS INTEGER), CAST(AVG(alloc_mem) AS INTEGER),
			   CAST(AVG(used_cpu_m) AS INTEGER), CAST(AVG(used_mem) AS INTEGER)
			 FROM node_samples WHERE ts<? GROUP BY 1,2`,
			`DELETE FROM node_samples WHERE ts<?`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// Expire deletes all data older than before, except the latest snapshot of
// each kind (current config state must always be available).
func (s *Store) Expire(before time.Time) error {
	b := before.Unix()
	return s.tx(func(tx *sql.Tx) error {
		stmts := []string{
			`DELETE FROM samples WHERE ts<?`,
			`DELETE FROM samples_5m WHERE ts<?`,
			`DELETE FROM node_samples WHERE ts<?`,
			`DELETE FROM node_samples_5m WHERE ts<?`,
			`DELETE FROM events WHERE ts<?`,
			`DELETE FROM assessments WHERE ends_at<?`,
			`DELETE FROM snapshots WHERE ts<?1 AND rowid NOT IN
			   (SELECT rowid FROM snapshots s WHERE ts=(SELECT MAX(ts) FROM snapshots WHERE kind=s.kind))`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q, b); err != nil {
				return err
			}
		}
		return nil
	})
}

// SizeBytes is the space the data occupies (pages in use, excluding free
// pages). The file itself never shrinks; freed pages are reused.
func (s *Store) SizeBytes() (int64, error) {
	var pages, free, size int64
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, err
	}
	pages -= free
	if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -w internal/store && go test ./internal/store/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "falcon-resource-test: add chart series, rollup, and retention

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: Assessment records

**Files:**
- Create: `internal/store/assessments.go`
- Test: `internal/store/assessments_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"testing"
	"time"
)

func TestAssessmentLifecycle(t *testing.T) {
	s := open(t)
	a, err := s.CreateAssessment(KindScan, t0, t0.Add(15*time.Minute), t0)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Status != StatusRunning {
		t.Fatalf("created: %+v", a)
	}
	run, ok, _ := s.RunningAssessment()
	if !ok || run.ID != a.ID {
		t.Fatalf("running: %+v %v", run, ok)
	}
	h, _ := s.CreateAssessment(KindHistory, t0.Add(-time.Hour), t0, t0)
	if h.Status != StatusComplete {
		t.Fatalf("history reports are complete on creation: %+v", h)
	}
	if err := s.SetAssessmentStatus(a.ID, StatusInterrupted); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.RunningAssessment(); ok {
		t.Fatal("no assessment should be running")
	}
	list, _ := s.ListAssessments()
	if len(list) != 2 || list[0].ID != h.ID {
		t.Fatalf("want newest first: %+v", list)
	}
	got, ok, _ := s.GetAssessment(a.ID)
	if !ok || got.Status != StatusInterrupted || !got.EndsAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("get: %+v", got)
	}
	if _, ok, _ := s.GetAssessment(999); ok {
		t.Fatal("missing id should return ok=false")
	}
	// frozen result: written once at completion, read back verbatim
	if _, ok, _ := s.AssessmentResult(h.ID); ok {
		t.Fatal("no result before SetAssessmentResult")
	}
	if err := s.SetAssessmentResult(h.ID, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if b, ok, _ := s.AssessmentResult(h.ID); !ok || string(b) != `{"x":1}` {
		t.Fatalf("result: %q %v", b, ok)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/store/ -run Assessment`
Expected: FAIL (`undefined: KindScan`)

- [ ] **Step 3: Implement**

```go
package store

import (
	"database/sql"
	"time"
)

const (
	KindScan    = "scan"    // timed run started from the button
	KindHistory = "history" // "report on the last X hours", complete on creation

	StatusRunning     = "running"
	StatusComplete    = "complete"
	StatusInterrupted = "interrupted"
)

type Assessment struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	EndsAt    time.Time `json:"ends_at"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) CreateAssessment(kind string, start, end, now time.Time) (Assessment, error) {
	status := StatusRunning
	if kind == KindHistory {
		status = StatusComplete
	}
	res, err := s.db.Exec(`INSERT INTO assessments (kind,started_at,ends_at,status,created_at) VALUES (?,?,?,?,?)`,
		kind, start.Unix(), end.Unix(), status, now.Unix())
	if err != nil {
		return Assessment{}, err
	}
	id, _ := res.LastInsertId()
	return Assessment{ID: id, Kind: kind, StartedAt: start, EndsAt: end, Status: status, CreatedAt: now}, nil
}

func (s *Store) SetAssessmentStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE assessments SET status=? WHERE id=?`, status, id)
	return err
}

// SetAssessmentResult freezes a completed report (JSON) so it survives
// retention: raw samples expire after 24h, saved reports must not.
func (s *Store) SetAssessmentResult(id int64, result []byte) error {
	_, err := s.db.Exec(`UPDATE assessments SET result=? WHERE id=?`, result, id)
	return err
}

func (s *Store) AssessmentResult(id int64) ([]byte, bool, error) {
	var b []byte
	err := s.db.QueryRow(`SELECT result FROM assessments WHERE id=? AND result IS NOT NULL`, id).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	return b, err == nil, err
}

func (s *Store) GetAssessment(id int64) (Assessment, bool, error) {
	a, err := scanAssessment(s.db.QueryRow(`SELECT id,kind,started_at,ends_at,status,created_at FROM assessments WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	return a, err == nil, err
}

func (s *Store) RunningAssessment() (Assessment, bool, error) {
	a, err := scanAssessment(s.db.QueryRow(`SELECT id,kind,started_at,ends_at,status,created_at FROM assessments WHERE status=? ORDER BY id DESC LIMIT 1`, StatusRunning))
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	return a, err == nil, err
}

func (s *Store) ListAssessments() ([]Assessment, error) {
	rows, err := s.db.Query(`SELECT id,kind,started_at,ends_at,status,created_at FROM assessments ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assessment
	for rows.Next() {
		a, err := scanAssessment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanAssessment(r scanner) (Assessment, error) {
	var a Assessment
	var st, en, cr int64
	err := r.Scan(&a.ID, &a.Kind, &st, &en, &a.Status, &cr)
	a.StartedAt, a.EndsAt, a.CreatedAt = time.Unix(st, 0), time.Unix(en, 0), time.Unix(cr, 0)
	return a, err
}
```

- [ ] **Step 4: Run all store tests**

Run: `gofmt -w internal/store && go test ./internal/store/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "falcon-resource-test: add assessment records

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
