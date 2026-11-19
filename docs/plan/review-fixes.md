# Review fixes: store v2 and analysis corrections (re-scoped)

Code review of Parts 1 and 2 (Tasks 0-8 as first built) found design defects:

- Peaks were inflated, because per-container bucket maxima were summed.
- Gap totals were wrong in both directions.
- Disk use per row was about 10x over budget.
- Chart queries scanned every raw row.
- `Expire` deleted saved reports and the snapshot that was in effect at the cutoff.
- Missing data could read as a pass.

This file is the contract for the fix, and it **supersedes the code blocks for Tasks 2-8** in `01-foundation.md` and `02-discovery-analysis.md`. Later tasks (03-08) call the API defined here.

## Scope cuts (user decision, 2026-11-19)

The tool is installed, used to validate that Falcon isn't too heavy, and removed. So v1 drops:

- **Persistence and long history:** no PVC, no `retentionDays`, no 5-minute rollups, no multi-day charts. Raw per-tick data only, kept up to 24h. On large clusters the disk guard trims the oldest data earlier.
- **Prometheus backfill** (old Task 18).
- **Falcon API sensor health** (old Task 17): no `SnapFalconAPI`, no `model.SensorHost`, no `checkSensorHealth`, no `SensorsKnown`.

Assessment ("run") durations are **15m, 30m (default), and 1h**.

## Principle

Aggregate at write time. Every number in a report comes from per-tick totals that were exact when written: peaks are the max of per-tick sums, averages are sum/ticks, gaps are expected ticks minus recorded ticks. Per-container rows are kept only for per-container peaks (lane check) and the per-node drill chart.

## Schema (`internal/store/store.go`)

Every usage table is `WITHOUT ROWID`, keyed by series then time, or by time when there is one row per tick.

| Table | Key | Columns | Written by |
|---|---|---|---|
| `series` | `id INTEGER PRIMARY KEY` | `node, ns, pod, container, component`, `UNIQUE(node,ns,pod,container,component)` | InsertTick (cached in memory, key -> id) |
| `nodes` | `id INTEGER PRIMARY KEY` | `name UNIQUE` | InsertTick (cached) |
| `samples` | `(sid, ts)` | `cpu_m, mem_bytes` | InsertTick, one row per container per tick |
| `node_samples` | `(nid, ts)` | `alloc_cpu_m, alloc_mem, used_cpu_m, used_mem, falcon_cpu_m, falcon_mem` (Falcon = non-self samples on that node) | InsertTick |
| `component_ticks` | `(ts, component)` | `cpu_m, mem_bytes, pods` (sum over that component's containers at the tick; includes `self`) | InsertTick |
| `cluster_ticks` | `ts` | `nodes, alloc_cpu_m, alloc_mem, used_cpu_m, used_mem, falcon_cpu_m, falcon_mem` (one row per successful tick, even with zero Falcon samples) | InsertTick |
| `events`, `snapshots`, `assessments` | unchanged | | |

Inserts use `ON CONFLICT DO NOTHING`, so a repeated tick can't double rows. `model.Sample.CPUMaxm`/`MemMax` are removed (they only existed for rollups and backfill).

## Connections and pragmas

- **Writer `*sql.DB`:** 1 connection, `_txlock=immediate`.
- **Reader `*sql.DB`:** 2 connections, `_pragma=query_only(1)`.
- **Both:** `journal_mode(WAL)`, `busy_timeout(5000)`, `synchronous(NORMAL)`, `cache_size(-2000)`, `journal_size_limit(16777216)`.
- **Writer only:** `PRAGMA auto_vacuum=INCREMENTAL` before the schema is created.
- **DSN:** the path is URL-escaped.

## Store API (exact names; later tasks call these)

```go
func Open(path string) (*Store, error)
func (s *Store) Close() error

// Writes
func (s *Store) InsertTick(ts time.Time, samples []model.Sample, nodes []model.NodeSample) error // one tx, all usage tables
func (s *Store) InsertEvent(e model.Event) error
func (s *Store) PutSnapshot(ts time.Time, kind model.SnapshotKind, v any) error

// Snapshots
func (s *Store) LatestSnapshot(kind model.SnapshotKind) (Snapshot, bool, error)
func (s *Store) SnapshotsBetween(kind model.SnapshotKind, from, to time.Time) ([]Snapshot, error)
func (s *Store) SnapshotAt(kind model.SnapshotKind, at time.Time) (Snapshot, bool, error) // latest with ts < at ([from,to) convention)

// Analysis reads: raw per-tick rows in [from, to).
type Tick struct {
    TS             int64
    CPUm, MemBytes int64
    Pods           int64
}
type ClusterTick struct {
    TS, Nodes                              int64
    AllocCPUm, AllocMem, UsedCPUm, UsedMem int64
    FalconCPUm, FalconMem                  int64
}
type NodeTick struct {
    TS                    int64
    AllocCPUm, AllocMem   int64
    FalconCPUm, FalconMem int64
}
type ContainerPeak struct {
    Component       model.Component
    Container       string
    CPUMaxm, MemMax int64
}
func (s *Store) ComponentTicks(from, to time.Time) (map[model.Component][]Tick, error) // includes model.Self
func (s *Store) ClusterTicks(from, to time.Time) ([]ClusterTick, error)
func (s *Store) EachNode(from, to time.Time, only []string, fn func(node string, t []NodeTick) error) error
    // streams one node at a time (one PK seek per node); only == nil means every node
func (s *Store) ContainerPeaks(from, to time.Time) ([]ContainerPeak, error) // non-self; sorted by component, container

// Chart reads (UI). step aligns buckets; a point is the time-weighted average over the bucket:
// series sum / cluster ticks in that bucket, so a component absent for part of a bucket counts as 0
// for that part, and pods that churned mid-bucket are not double-counted. step < 1s is an error.
type Point struct{ TS, CPUm, MemBytes int64 }                                // json: ts, cpu_m, mem_bytes
type ClusterPoint struct{ TS, AllocCPUm, AllocMem, UsedCPUm, UsedMem int64 } // unchanged json
func (s *Store) ComponentSeries(from, to time.Time, step time.Duration, node string) (map[model.Component][]Point, error)
    // node == "": from component_ticks. node != "": from that node's series rows in samples, grouped by component.
    // Self is excluded from both.
func (s *Store) ClusterSeries(from, to time.Time, step time.Duration) ([]ClusterPoint, error)
func StepFor(d time.Duration) time.Duration // never < 15s; about 240 points per chart

// Raw reads kept for tests and small windows
func (s *Store) Samples(from, to time.Time) ([]model.Sample, error) // raw samples joined to series
func (s *Store) NodeSamples(from, to time.Time) ([]model.NodeSample, error)
func (s *Store) Events(from, to time.Time) ([]model.Event, error)

// Retention
func (s *Store) Expire(before time.Time) error
    // deletes usage rows ts < before in every usage table (chunked, one tx per chunk), events < before,
    // snapshots < before EXCEPT the newest per kind below the cutoff, series/nodes rows with no remaining
    // samples; NEVER assessments; then PRAGMA incremental_vacuum
func (s *Store) PruneAssessments(keep int) error  // keeps the newest `keep` assessments, deletes the rest
func (s *Store) Earliest() (time.Time, bool, error) // oldest ts in cluster_ticks (disk guard)
func (s *Store) SizeBytes() (int64, error)          // page_count*page_size + size of the -wal file

// Assessments: unchanged API (CreateAssessment returns the LastInsertId error)
```

Removed: `InsertSamples`, `InsertNodeSamples`, `BucketedSamples`, `BucketedNodeSamples`, `Rollup`, every `*_5m` table.

## Analysis (`internal/analyze`)

```go
type LoadOpts struct {
    From, To   time.Time
    Interval   time.Duration
    KACPresent bool
    Nodes      bool // compute per-node stats (reports: true; /api/live checks: false)
}
func Load(s *store.Store, o LoadOpts) (Input, error)
```

`Load` reads raw ticks for any window (up to the 24h retention). It fills `Input` with component ticks, cluster ticks, container peaks, events, the DaemonSet timeline, and webhooks + specs from `SnapshotAt(kind, To)`. `Input.Nodes []NodeStat` is computed with `EachNode` and a pure `nodeStat(name, ticks)` when `o.Nodes` is set; otherwise only the nodes named in pressure or eviction events, which the crowd check needs.

`Analyze(in Input) Report` stays pure. `Report` JSON keys stay the same (the UI and report template depend on them); fields may be added.

Corrections:

- **Component and cluster stats:** avg = sum / cluster ticks (a component missing at a tick counts as 0). Peak = max per-tick sum (exact). p50/p95 over per-tick values.
- **Gaps:** expected = floor(window / interval), got = cluster ticks, gap = max(0, expected - got) x interval. No ticks at all: gap = whole window. Interval <= 0: gap = 0.
- **Measured span:** add `Method.Measured` (ticks x interval). The headline uses the measured span and says "(X requested)" when it's under 90% of the window.
- **Missing data never reads as good:**
  - No cluster ticks: headline "Falcon CPU and memory were not measured: no metrics samples in this window."
  - No node allocatable: share is "not measured", never "0.0%".
  - Self usage with no samples says "not measured".
- **Stable:** falconSeen = any component tick, OR any spec, OR any Falcon event. Restart and OOM events count even when metrics are absent.
- **Lane:** uses `ContainerPeaks`. Containers without a memory limit each get a detail line saying so; the summary says "every container that has a limit". An `EventOOMKill` on a Falcon pod of that component is a Fail even if no sample reached the limit. Details sorted by (component, container).
- **Crowd:** per-node CPU and memory shares. Show the share matching the pressure type: memory for MemoryPressure and evictions, both otherwise. Drop the fixed "the cause is likely another workload" line. When there are no Falcon samples but a sensor DaemonSet exists, treat every node as a Falcon node.
- **Coverage:** last-snapshot total Desired of 0 is Fail ("The sensor DaemonSet is scheduled on no nodes"). Desired below the last cluster tick's `Nodes` is Review, with the difference in the details.
- **Admission:** `model.WebhookState` gains `Component model.Component` (the collector sets it by matching `clientConfig.service` namespace/name against discovered KAC or injector workloads). Only KAC hooks are graded; injector hooks are listed as "Container Sensor injector webhook (not graded)". No KAC hooks: Not measured.
- **Sensor health check:** removed (Falcon API cut). The checks list is Stable, Lane, Crowd, Coverage, Admission.
- **Nodes:** `Cluster.Nodes` = `Nodes` of the last cluster tick. The nodes table sorts worst first by max(CPUShareP95, MemShareP95).
- **Reservation:** DaemonSet Sensor uses `Desired` from the last DaemonSet snapshot; other components use `Pods` of the last tick.
- **Percentile:** `int(math.Ceil(p*float64(n))) - 1`, clamped.
- **Durations:** one formatter, `HumanDuration`, for every user-facing duration.

## Discovery (`internal/discover`)

- Add `"falcon-image-analyzer": model.IAR` to the name map.
- Add the Operator label `crowdstrike.com/component`:

  | `crowdstrike.com/component` | Component |
  |---|---|
  | `kernel_sensor` | DaemonSetSensor |
  | `admission_controller` | KAC |
  | `container_sensor` | Injector |
  | `falcon-imageanalyzer` | IAR |

- Kind rules: only DaemonSetSensor (must be a DaemonSet) and Injector (must be a Deployment) are kind-restricted. IAR may be a Deployment or a DaemonSet.
- Skip workloads whose name ends in `-node-cleanup` (the falcon-sensor chart's post-delete hook).

## Call-site changes for later tasks (03-08)

- **Collector fast tick:** `c.st.InsertTick(now, samples, nodes)`.
- **Collector:** sets `WebhookState.Component`. Restart events record the delta: one event per increment, capped at 10 per tick.
- **Slow tick retention:** `Expire(now - 24h)`, then `PruneAssessments(100)`.
- **`report.Build`:** `analyze.Load(st, analyze.LoadOpts{..., Nodes: true})`; chart series from `ComponentSeries(from, to, StepFor(...), "")`.
- **`liveChecks`:** `Load(..., Nodes: false)`.
- **`guardDisk`:** over 80% of `storage.sizeLimit`, expire the oldest 25% of the span between `Earliest()` and now; repeat up to 4 times per call until under the limit or the span is under 1h.
- **Assessment durations:** `15m | 30m | 1h` (default `30m`), plus `EXTRA_SCAN_DURATIONS` for e2e. Lookback reports ("analyze the last N"): `15m | 1h | 6h | 24h`, capped at what's retained.
- **Chart:** no persistence, `RETENTION_DAYS`, `PERSISTENCE`, `falconApi`, or `prometheus` values; NetworkPolicy egress is API server + DNS only. Remove Part 5 (`05-integrations.md`) entirely.
- **UI:** no Falcon API or Prometheus bits; live chart ranges 15m / 1h / 6h / 24h.
