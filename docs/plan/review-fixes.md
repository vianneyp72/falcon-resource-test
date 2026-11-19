# Review fixes: store v2 and analysis corrections

Code review of Parts 1 and 2 (after Tasks 0-8 were built) found design defects in the plan:

- Peaks were inflated on windows over 2h, because per-container bucket maxima were summed.
- Rollups double-counted pod churn, mis-weighted the boundary between raw and rolled-up rows, and truncated averages.
- Gap totals were wrong in both directions.
- Disk use per row was about 10x over budget.
- Chart queries scanned every raw row.
- `Expire` deleted saved reports and the snapshot that was in effect at the cutoff.
- Missing data could read as a pass.

This file is the contract for the fix. Plan files 01-05 are rewritten to match it, so it is the single source of truth while they change.

## Principle

Aggregate at write time. Every number in a report comes from per-tick totals that were exact when they were written. Peaks are the max of per-tick sums, averages are sum/ticks, and gaps are expected ticks minus recorded ticks. Per-container rows are kept only for what genuinely needs them: per-container peaks for the lane check, and the per-node drill chart.

## Schema (`internal/store/store.go`)

Every usage table is `WITHOUT ROWID`, with a primary key that leads with the series key and then time, or with time when the table holds one row per tick. Rollup tables store **sum, tick count, and max**, never a truncated average.

| Table | Key | Columns | Written by |
|---|---|---|---|
| `series` | `id INTEGER PRIMARY KEY` | `node, ns, pod, container, component`, `UNIQUE(node,ns,pod,container,component)` | InsertTick (cached in memory, map key->id) |
| `nodes` | `id INTEGER PRIMARY KEY` | `name UNIQUE` | InsertTick (cached) |
| `samples` | `(sid, ts)` | `cpu_m, mem_bytes` | InsertTick, one row per container per tick |
| `samples_5m` | `(sid, ts)` | `n, cpu_sum, mem_sum, cpu_max, mem_max` | Rollup, InsertRollups |
| `node_samples` | `(nid, ts)` | `alloc_cpu_m, alloc_mem, used_cpu_m, used_mem, falcon_cpu_m, falcon_mem` (Falcon = non-self samples on that node) | InsertTick |
| `node_5m` | `(nid, ts)` | `n`, sums of the six columns above, `falcon_cpu_max, falcon_mem_max` | Rollup, InsertRollups |
| `component_ticks` | `(ts, component)` | `cpu_m, mem_bytes, pods` (sum across that component's containers at the tick; includes `self`) | InsertTick |
| `component_5m` | `(ts, component)` | `n, cpu_sum, mem_sum, cpu_max, mem_max, pods_max` | Rollup, InsertRollups |
| `cluster_ticks` | `ts` | `nodes, alloc_cpu_m, alloc_mem, used_cpu_m, used_mem, falcon_cpu_m, falcon_mem` (always one row per successful tick, even with zero Falcon samples) | InsertTick |
| `cluster_5m` | `ts` | `n, nodes_max`, sums of the six, `falcon_cpu_max, falcon_mem_max` | Rollup, InsertRollups |
| `events`, `snapshots`, `assessments` | unchanged | | |

Raw and 5m ranges never overlap: `Rollup` moves rows (insert into `*_5m`, delete raw) in the same transaction. Readers `UNION ALL` the two tables with no `MIN(ts)` trick.

Inserts use `ON CONFLICT DO NOTHING` (or `DO UPDATE` where a merge is meaningful), so a duplicate tick or a repeated backfill can't double rows.

## Connections and pragmas

- **Writer `*sql.DB`:** 1 connection, `_txlock=immediate`.
- **Reader `*sql.DB`:** 2 connections, `_pragma=query_only(1)`.
- **Both:** `journal_mode(WAL)`, `busy_timeout(5000)`, `synchronous(NORMAL)`, `cache_size(-2000)`, `journal_size_limit(16777216)`.
- **Writer only:** sets `PRAGMA auto_vacuum=INCREMENTAL` before the schema is created.
- **DSN:** the path is URL-escaped.
- **Contexts:** not added to the API, because queries against aggregate tables are small and fast.

## Store API (exact names; later tasks call these)

```go
func Open(path string) (*Store, error)
func (s *Store) Close() error

// Writes
func (s *Store) InsertTick(ts time.Time, samples []model.Sample, nodes []model.NodeSample) error // one tx, all usage tables
func (s *Store) InsertRollups(samples []model.Sample, nodes []model.NodeSample, interval time.Duration) error
    // Prometheus backfill: each Sample/NodeSample is one 5m bucket (TS aligned), CPUm/MemBytes = bucket avg,
    // CPUMaxm/MemMax = bucket max (0 -> use avg). n = 5m/interval. Writes all *_5m tables.
    // Component/cluster sums are summed avgs x n. Their max is the sum of per-container maxes, flagged as an
    // upper bound (the backfill methodology note already says peaks are smoothed).
func (s *Store) InsertEvent(e model.Event) error
func (s *Store) PutSnapshot(ts time.Time, kind model.SnapshotKind, v any) error

// Snapshots (unchanged)
func (s *Store) LatestSnapshot(kind model.SnapshotKind) (Snapshot, bool, error)
func (s *Store) SnapshotsBetween(kind model.SnapshotKind, from, to time.Time) ([]Snapshot, error)
func (s *Store) SnapshotAt(kind model.SnapshotKind, at time.Time) (Snapshot, bool, error) // latest with ts < at (exclusive, [from,to) convention)

// Analysis reads. step == 0 means raw ticks (N=1 per row, Max = value); step > 0 buckets by step
// (aligned to step) over raw + 5m rows. step must be 0 or >= 5m when the window reaches rolled-up data,
// and step < 1s other than 0 is an error.
type Bucket struct {
    TS             int64 // bucket start, unix seconds
    N              int64 // ticks in the bucket that had this series
    CPUSum, MemSum int64 // sum over those ticks
    CPUMax, MemMax int64 // largest single-tick value
    PodsMax        int64 // component buckets only
}
type ClusterBucket struct {
    TS, N                                           int64
    NodesMax, NodesLast                             int64
    AllocCPUSum, AllocMemSum, UsedCPUSum, UsedMemSum int64
    FalconCPUSum, FalconMemSum                      int64
    FalconCPUMax, FalconMemMax                      int64
}
func (s *Store) ComponentBuckets(from, to time.Time, step time.Duration) (map[model.Component][]Bucket, error) // includes model.Self
func (s *Store) ClusterBuckets(from, to time.Time, step time.Duration) ([]ClusterBucket, error)
func (s *Store) EachNode(from, to time.Time, step time.Duration, only []string, fn func(node string, b []NodeBucket) error) error
    // streams one node at a time (one PK seek per node, so memory is one node's buckets); only == nil means every node
type NodeBucket struct {
    TS, N                                    int64
    AllocCPUSum, AllocMemSum                 int64
    FalconCPUSum, FalconMemSum               int64
    FalconCPUMax, FalconMemMax               int64
}
type ContainerPeak struct {
    Component      model.Component
    Container      string
    CPUMaxm, MemMax int64
}
func (s *Store) ContainerPeaks(from, to time.Time) ([]ContainerPeak, error) // max over raw + 5m per (component, container); sorted by component, container

// Chart reads (UI). Point values are time-weighted averages over the bucket:
// series sum / cluster ticks in that bucket, so a component absent for part of a bucket counts as 0 for that part,
// and two pods of one component that churned mid-bucket are not double-counted.
type Point struct{ TS, CPUm, MemBytes int64 }        // json: ts, cpu_m, mem_bytes (unchanged)
type ClusterPoint struct{ TS, AllocCPUm, AllocMem, UsedCPUm, UsedMem int64 } // unchanged json
func (s *Store) ComponentSeries(from, to time.Time, step time.Duration, node string) (map[model.Component][]Point, error)
    // node == "": from component_ticks/component_5m. node != "": from that node's series rows in samples/samples_5m,
    // grouped by component (denominator is still cluster ticks per bucket). Self is excluded from both.
func (s *Store) ClusterSeries(from, to time.Time, step time.Duration) ([]ClusterPoint, error)
func StepFor(d time.Duration) time.Duration // unchanged; never returns < 15s; returns >= 5m for d > 2h

// Raw reads kept for tests and small windows only (analysis does not use them)
func (s *Store) Samples(from, to time.Time) ([]model.Sample, error)      // raw samples joined to series
func (s *Store) NodeSamples(from, to time.Time) ([]model.NodeSample, error)
func (s *Store) Events(from, to time.Time) ([]model.Event, error)

// Retention
func (s *Store) Rollup(cutoff time.Time) error   // cutoff aligned down to 5m; moves raw < cutoff into *_5m; one tx per hour of data; idempotent
func (s *Store) Expire(before time.Time) error
    // deletes usage rows ts < before in every table (chunked, one tx per chunk), events < before,
    // snapshots < before EXCEPT the newest per kind below the cutoff, series/nodes rows with no remaining samples;
    // NEVER assessments; then PRAGMA incremental_vacuum
func (s *Store) PruneAssessments(keep int) error // keeps the newest `keep` assessments (any status), deletes the rest
func (s *Store) Earliest() (time.Time, bool, error) // oldest ts in cluster_ticks or cluster_5m
func (s *Store) SizeBytes() (int64, error)      // page_count*page_size + size of the -wal file (os.Stat); what the kubelet counts

// Assessments: unchanged API (CreateAssessment returns the LastInsertId error)
```

`EarliestSample` (Part 5) is renamed `Earliest`. `InsertSamples` and `InsertNodeSamples` are removed in favor of `InsertTick`. `BucketedSamples` and `BucketedNodeSamples` are removed.

## Analysis (`internal/analyze`)

```go
type LoadOpts struct {
    From, To     time.Time
    Interval     time.Duration
    KACPresent   bool
    SensorsKnown bool
    Nodes        bool // compute per-node stats (reports: true; /api/live checks: false)
}
func Load(s *store.Store, o LoadOpts) (Input, error)
```

`Load` picks `step = 0` (raw) when the window is 2h or shorter, otherwise 5m. It fills `Input` as follows:

- From the store: component buckets, cluster buckets, container peaks, events, and the DaemonSet timeline. Webhooks, specs and sensors come from `SnapshotAt(kind, To)`.
- `Input.Nodes []NodeStat`, computed with `EachNode` and a pure `nodeStat(name, buckets)` function, when `o.Nodes` is set.
- Otherwise, only the shares for nodes named in pressure or eviction events, which the crowd check needs.

`Analyze(in Input) Report` stays pure, and the `Report` JSON keys stay the same: the UI and report template depend on them. Only fields are added.

The corrections:

- **Component and cluster stats:**
  - avg = ΣCPUSum / Σ cluster N. A component that's missing at a tick counts as 0 for that tick.
  - peak = max of Max over buckets, which is exact.
  - p50 and p95 come from per-bucket averages (Sum/N). On raw windows these are exact per-tick values. On 5m windows, `Method.Notes` gets the line "Percentiles over windows longer than 2h use 5-minute averages; peaks are exact."
- **Gaps:**
  - expected = floor(window / interval), and got = Σ cluster N.
  - gap = max(0, expected - got) × interval.
  - With no ticks at all, gap = the whole window.
  - An interval of 0 or less gives gap = 0.
- **Measured span:** add `Method.Measured` (ticks × interval). The headline uses the measured span, and says "(X requested)" when it's under 90% of the window.
- **Missing data never reads as good** (spec principle 3):
  - With no cluster ticks, the headline is "Falcon CPU and memory were not measured: no metrics samples in this window."
  - With no node allocatable, the share is "not measured", never "0.0%".
  - "No Falcon usage was recorded" becomes "Usage not measured: no metrics samples in this window."
  - Self usage with no samples says "not measured".
- **Stable:** falconSeen = any component bucket, OR any spec, OR any Falcon event. Restart and OOM events count even when metrics are absent.
- **Lane:**
  - Uses `ContainerPeaks`. Containers without a memory limit each get a detail line saying so, and the summary becomes "every container that has a limit".
  - An `EventOOMKill` on a Falcon pod of that component is a hit (Fail), whether or not a sample reached the limit.
  - Details are in deterministic order, with a tuple sort on (component, container).
- **Crowd:** take the CPU and memory shares per node. Show the share that matches the pressure type: memory for MemoryPressure and evictions, both otherwise. Drop the fixed "the cause is likely another workload" line. When there are no Falcon samples but a sensor DaemonSet exists, treat every node as a Falcon node.
- **Coverage:** a last-snapshot total Desired of 0 is Fail ("The sensor DaemonSet is scheduled on no nodes"). When Desired is below `NodesLast`, the result is Review, with the difference in the details.
- **Admission:**
  - `model.WebhookState` gains `Component model.Component`. The collector sets it by matching the webhook's `clientConfig.service` namespace and name against the discovered KAC or injector workloads.
  - Only KAC hooks are graded. Injector hooks are listed as details with "Container Sensor injector webhook (not graded)".
  - No KAC hooks → Not measured.
- **Sensor health:** when the API is known but returns 0 hosts, or fewer hosts than `NodesLast`, the result is Review ("N cluster nodes have no matching sensor host").
- **Nodes:**
  - `Cluster.Nodes` = `NodesLast` of the final cluster bucket, not the count of distinct nodes.
  - The nodes table sorts worst first by max(CPUShareP95, MemShareP95).
- **Reservation:** the DaemonSet Sensor uses `Desired` from the last DaemonSet snapshot. Other components use `PodsMax` of the last bucket.
- **Percentile:** `int(math.Ceil(p*float64(n))) - 1`, clamped.
- **Durations:** one formatter, `HumanDuration`, for every user-facing duration.

## Discovery (`internal/discover`)

- Add `"falcon-image-analyzer": model.IAR` to the name map.
- Add the Operator label `crowdstrike.com/component`. Its values map as follows:

  | `crowdstrike.com/component` | Component |
  |---|---|
  | `kernel_sensor` | DaemonSetSensor |
  | `admission_controller` | KAC |
  | `container_sensor` | Injector |
  | `falcon-imageanalyzer` | IAR |

- Kind rules: only DaemonSetSensor (must be a DaemonSet) and Injector (must be a Deployment) are kind-restricted. IAR is allowed as either a Deployment or a DaemonSet.
- Skip workloads whose name ends in `-node-cleanup` (the falcon-sensor chart's post-delete hook).

## Collection and server (plans 03 and 04): call-site changes

- **Collector fast tick:** `c.st.InsertTick(now, samples, nodes)`.
- **Collector:** sets `WebhookState.Component`. The restart event records the delta: one event per increment, capped at 10 per tick.
- **Slow tick retention:**
  - `Rollup(now - rawRetention)`, then `Expire(now - retention)`, then `PruneAssessments(100)`.
- **`report.Build`:** `analyze.Load(st, analyze.LoadOpts{..., Nodes: true})`. Its series come from `ComponentSeries(from, to, StepFor(...), "")`.
- **`liveChecks`:** `Load(..., Nodes: false)`.
- **`guardDisk`:** when the database is over 80% of the limit, it expires the oldest 25% of the span between `Earliest()` and now. It repeats, up to 4 times per call, until the database is under the limit or the span is under 1h.
- **Part 5 backfill:** `Earliest()` and `InsertRollups(samples, nodes, interval)`.
