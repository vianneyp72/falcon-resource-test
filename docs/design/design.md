# Falcon Resource Test — Design Spec

**Date:** 2026-11-19
**Status:** Approved for planning
**Working name:** `falcon-resource-test`

## 1. Purpose

A lightweight, in-cluster tool that shows a customer exactly how much CrowdStrike Falcon costs and how it behaves in their Kubernetes cluster, so a platform/SRE lead can confidently say "I'm okay with CrowdStrike in production."

- **Primary view: live utilization.** Open the UI and see Falcon's current CPU/memory, health, and coverage.
- **On-demand assessment.** A button runs a scan over a chosen window (5m / 15m / 1h / 24h) and produces a saved report with averages, p95, peaks, health checks, and config facts, exportable as a single HTML file.
- **Audience:** customer platform engineers (self-serve during a POV or pre-prod rollout), SEs running it live with a customer, and customers who leave it installed to watch Falcon over time. Any DevOps/GitOps engineer should recognize it as "just a Helm chart."

### Non-goals (v1)

- EKS Fargate / sidecar-only clusters (no kubelet metrics on Fargate). Shown as "not measurable in v1."
- Serverless containers (ECS Fargate, ACI, Cloud Run).
- CPU throttling and KAC admission-latency metrics (need `nodes/proxy` and `/metrics` RBAC that security reviewers push back on).
- Built-in load generator (would require write RBAC to create Jobs).
- Multi-cluster aggregation.

## 2. Principles

1. **Read-only.** No write verbs, no Secrets, no `pods/exec`. A security reviewer should approve the RBAC in one read.
2. **As light as possible.** The tool must never be the thing that skews the numbers. It reports its own usage next to Falcon's.
3. **Never a false pass.** Missing data shows "Not measured" with the reason, not a green check.
4. **Honest methodology.** Every report states its window, cluster load during it, and known measurement limits.
5. **CrowdStrike UI/UX.** Same brand system as the labs webapp (dark-first, `#ED1C24` accent; system font stack, since the public repo can't bundle Sharp Sans).

## 3. Architecture

A single static Go binary, deployed as one Deployment (1 replica) via one Helm chart.

```

├── cmd/falcon-resource-test/        entrypoint
├── internal/
│   ├── discover/         find Falcon components (labels, images, namespaces)
│   ├── collect/          metrics.k8s.io sampler + K8s object watchers
│   ├── store/            SQLite time-series + rollups + retention
│   ├── analyze/          percentiles, shares, health checks, headline
│   ├── report/           server-rendered HTML report (inline SVG charts)
│   ├── falconapi/        OPTIONAL: Falcon Hosts API (sensor status, RFM)
│   ├── prom/             OPTIONAL: Prometheus history backfill
│   └── web/              HTTP API + embedded UI (go:embed)
├── ui/                   React + Vite SPA, brand tokens copied from webapp/
├── chart/                Helm chart (the one install artifact)
├── scripts/assess.sh     install → wait → port-forward → open browser
├── Dockerfile            distroless/static, nonroot, multi-arch (amd64, arm64)
└── README.md             short pointer to the site guide
```

CI lives at the repo root: `.github/workflows/falcon-resource-test.yml`.

### Components and interfaces

| Unit | Does | Depends on |
|---|---|---|
| `discover` | Returns the set of Falcon workloads: `{component, namespace, kind, name, selector}`. Re-runs every 5 min. | K8s API (list DaemonSets/Deployments) |
| `collect` | Every interval, writes pod + node samples; watches pods, nodes, DaemonSets, webhooks, eviction events; writes events + config snapshots. | `discover`, `store`, K8s API |
| `store` | Append samples/events/snapshots; query series by range + resolution; roll up and expire data. | SQLite (pure-Go driver, no CGO) |
| `analyze` | Given a time range, computes stats, health checks, headline. Pure functions over `store` queries. | `store` |
| `report` | Renders an `analyze` result to self-contained HTML. | `analyze` |
| `web` | JSON API + static UI. | `analyze`, `store`, `report` |

## 4. Discovery

A workload is Falcon if any of these match (first match wins for component type):

| Component | Match |
|---|---|
| DaemonSet Sensor | image name `falcon-sensor`, or label `app.kubernetes.io/name=falcon-sensor` |
| KAC | image `falcon-kac`, or label `app.kubernetes.io/name=falcon-kac` |
| IAR | image `falcon-imageanalyzer`, or label `app.kubernetes.io/name=falcon-imageanalyzer` |
| Container Sensor injector | image `falcon-container` on a Deployment (the injector) |
| Falcon Operator | image `falcon-operator` |

- Works for Helm and Operator installs. Discovery lists DaemonSets/Deployments cluster-wide once per 5 min (cheap, server-side), then **all watches are scoped to the discovered Falcon namespaces only**.
- Overridable in values: `discovery.namespaces`, `discovery.extraSelectors`.
- Injected `crowdstrike-falcon-container` sidecars on regular (non-Fargate) nodes are out of scope for v1 (they'd require watching all namespaces).

## 5. Data collection

Default interval 15s (`collection.interval`; recommend 30–60s above 200 nodes).

| Signal | Source | Per tick cost |
|---|---|---|
| CPU (millicores), memory working set per Falcon container | `metrics.k8s.io` PodMetrics, one list per Falcon namespace | 1 call / Falcon ns |
| Node usage + allocatable (cluster load, share-of-node) | `metrics.k8s.io` NodeMetrics + Node watch | 1 call |
| Restarts, last termination reason (OOMKilled), readiness | Pod watch (Falcon ns) | watch |
| Node pressure (Memory/Disk/PID) | Node conditions (Node watch) | watch |
| Evictions | Event watch, cluster-wide, field selector `reason=Evicted` (server-side filtered) | watch |
| DaemonSet desired/ready/updated | DaemonSet watch (Falcon ns) | watch |
| KAC webhook `failurePolicy`, `timeoutSeconds`, namespace selector | Mutating/ValidatingWebhookConfiguration watch | watch |
| Requests/limits, priorityClass, sensor version (image tag), backend (kernel/bpf from `FALCONCTL_OPT_BACKEND` env/args) | Pod spec snapshot | on change |
| Tool's own CPU/mem | its own PodMetrics | included above |

Failed or slow calls skip that sample, which shows as a gap in charts. Values are never interpolated.

### Optional integrations (off by default)

- **Falcon API** (`falconApi.enabled`, `falconApi.existingSecret` with `client_id`/`client_secret`, mounted as a file; Hosts: Read scope): per-node sensor version, online/last-seen, reduced functionality mode. Matched to nodes by hostname. Polled every 5 min.
- **Prometheus backfill** (`prometheus.url`): on startup, queries `container_cpu_usage_seconds_total` and `container_memory_working_set_bytes` for Falcon namespaces over `prometheus.backfillDays` (default 7) and imports at 5m resolution.

## 6. Storage

Embedded SQLite (`modernc.org/sqlite`, pure Go), WAL mode, page cache capped at 4 MiB.

Tables:
- `samples(ts, node, ns, pod, container, component, cpu_m, mem_bytes)`
- `node_samples(ts, node, alloc_cpu_m, alloc_mem_bytes, used_cpu_m, used_mem_bytes)`
- `events(ts, kind, ns, object, reason, message)`: restart, oomkill, crashloop, eviction, pressure, ready_change
- `snapshots(ts, kind, json)`: daemonset, webhook, pod_spec, falcon_api
- `assessments(id, started_at, ends_at, duration, status, created_at)`: status ∈ running | complete | interrupted

Retention:
- Raw 15s samples: 24h (default, no persistence) or 48h (with persistence), then rolled up to 5m (avg + max).
- Rollups: 24h without persistence; `persistence.retentionDays` (default 30) with persistence.
- Default volume: `emptyDir` with `sizeLimit: 256Mi`. `persistence.enabled: true` → PVC (default 1Gi).

## 7. Analysis

For any time range `[from, to]`:

**Headline sentence:**
> Over {duration} on {N} nodes, Falcon used {cpu%} of cluster CPU and {mem%} of memory, with {restarts} restarts and {ready}/{eligible} node coverage.

`cpu%` = avg Falcon CPU used / total node allocatable CPU (same for memory).

**Cost (per component, per node, cluster total):**
- Reserved: sum of requests, and as % of allocatable.
- Used: avg, p50, p95, peak for CPU and memory.
- Worst node: highest p95 Falcon share-of-node, called out.

**Health checks (objective; no invented thresholds):**

| Check | Pass | Fail | Not measured when |
|---|---|---|---|
| Stable | 0 restarts, 0 OOMKills, 0 CrashLoops | any | never |
| Stays in its lane | peak memory < memory limit for every container (CPU limits noted if set) | any container hit its limit | no limits set → "No limits set" (informational) |
| Doesn't crowd workloads | no evictions and no Memory/Disk/PID pressure on any node running Falcon | a Falcon pod was evicted → **Fail**; pressure or non-Falcon evictions on a Falcon node → **Review** (amber, lists node, time, and Falcon's share of that node, since the cause may be another workload) | never |
| Full coverage | DaemonSet ready == desired for the whole window | any shortfall (shows duration and nodes) | no DaemonSet found |
| Safe admission control | KAC `failurePolicy: Ignore` | `Fail` → shown as **Review** (amber) with explanation, not red | no KAC |
| Sensor healthy (Falcon API) | all nodes online, none in RFM | any RFM or stale | API not enabled |

**Configuration facts (not graded):** sensor version per component, backend (kernel/bpf), requests/limits, priorityClass, webhook timeout, chart/operator install method.

**Methodology notes (always present):**
- Window length and cluster average CPU utilization during it.
- If cluster avg CPU < 10% of allocatable: "Idle-cluster baseline; rerun during representative traffic."
- "Part of the sensor's work runs inside the syscalls of the workloads it watches and is billed to those workloads, so pod metrics slightly undercount Falcon's total cost."
- The tool's own avg CPU/memory over the window.
- Any gaps (missed samples) and their total duration.

## 8. UI and UX

React + Vite SPA, embedded into the binary, gzip-served. Charts via **uPlot** (~45 KB, fast, low memory). Brand tokens copied from `webapp/src/styles` (CS Mode A, light/dark toggle).

### Live (landing page)
- Header: cluster name, overall status dot, tool's own usage ("This tool: 6m CPU / 38Mi"), **Run assessment** button.
- KPI tiles: Falcon CPU (% of cluster + millicores), Falcon memory (% + bytes), restarts (24h), node coverage.
- Stacked area chart: Falcon CPU and memory by component, cluster load as a background line. Range: 15m / 1h / 6h / 24h (up to retention with persistence).
- Component table: pods, CPU now, mem now, request/limit, status. Click → component drill-down graph.
- Nodes table, worst first. Click → node drill-down.
- Live health checks panel.
- Polls `/api/live` every 15s only while the tab is visible (`document.visibilityState`).

### Assessments
- **Run assessment** dialog: duration 5m / 15m (default) / 1h / 24h, with a one-line note that longer runs under real traffic are more trustworthy.
- Runs server-side; the tab can be closed. A banner shows progress.
- **Report on the last X hours**: builds a report instantly from existing history.
- One assessment at a time. If the tool restarts mid-window, the assessment is marked `interrupted` and never shown as a result.
- Assessments tab lists saved reports; each opens a report view with an **Export HTML** button.

### Export
Single self-contained HTML file rendered server-side by Go `html/template`, charts as inline SVG, CSS inlined, no JS. Prints cleanly to PDF. Includes headline, cost tables, charts, health checks, config facts, methodology notes, and generation metadata.

### Setup / empty states
- `/setup` page runs preflight checks: metrics-server reachable, each RBAC permission (via SelfSubjectAccessReview), Falcon found, optional integrations reachable.
- metrics-server missing → banner with per-cloud install command; usage panels show "Not measured"; health/config still work.
- No Falcon found → empty state listing what was searched and how to set `discovery.namespaces`.

## 9. HTTP API

| Method + path | Returns |
|---|---|
| `GET /api/live` | current KPIs, components, nodes, health checks, self usage |
| `GET /api/series?from&to&component&node` | time series at auto-selected resolution |
| `GET /api/report?from&to` | analysis JSON for an arbitrary range |
| `POST /api/assessments` `{duration}` | create; 409 if one is running |
| `GET /api/assessments`, `GET /api/assessments/{id}` | list / detail (analysis JSON when complete) |
| `GET /api/assessments/{id}/export` | HTML report download |
| `GET /api/setup` | preflight results |
| `GET /healthz`, `GET /readyz` | probes |

## 10. Resource budget (hard requirement, CI-enforced)

| | Target | Helm default |
|---|---|---|
| CPU | < 10m average | request 10m, limit 100m |
| Memory | < 48Mi on a 50-node cluster | request 32Mi, limit 128Mi |
| Image | < 25 MB | |
| Disk | ~5–10 MB/day on 12 nodes | emptyDir 256Mi |

How:
- Watches scoped to Falcon namespaces; only Nodes and filtered eviction Events are cluster-wide.
- Informer caches strip `managedFields` and unused fields (transform func).
- Samples go to disk (SQLite), fixed page cache; memory doesn't grow with history.
- No work when idle: reports computed on request; UI polls only while visible.
- `GOMEMLIMIT` set from the memory limit via downward API; `GOGC` tuned.

## 11. Security

- **ServiceAccount + ClusterRole (read-only):** `get/list/watch` on `pods`, `nodes`, `events`, `namespaces`, `apps/daemonsets`, `apps/deployments`, `admissionregistration.k8s.io/mutatingwebhookconfigurations`, `validatingwebhookconfigurations`; `get/list` on `metrics.k8s.io` `pods`, `nodes`; `create` on `authorization.k8s.io/selfsubjectaccessreviews` (preflight only; standard for any authenticated user).
- **Never:** Secrets, `pods/exec`, `nodes/proxy`, any write verb on workloads.
- **Pod security:** `runAsNonRoot`, `readOnlyRootFilesystem`, drop ALL capabilities, `allowPrivilegeEscalation: false`, seccomp `RuntimeDefault`. Passes Pod Security Standards `restricted`.
- **NetworkPolicy** (on by default): ingress only on the UI port; egress only to the API server and DNS, plus Falcon API / Prometheus when enabled.
- **Access:** ClusterIP Service; reached via `kubectl port-forward`. Optional Ingress off by default.
- **Supply chain:** image signed with cosign (keyless), SBOM attached.

## 12. Distribution and install

- **Image:** `ghcr.io/vianneyp72/falcon-resource-test:<version>`, multi-arch.
- **Chart:** published as OCI to `oci://ghcr.io/vianneyp72/charts/falcon-resource-test`.
- **One-liner (SE on a call):** `scripts/assess.sh` → `helm upgrade --install`, wait for rollout, port-forward, open browser. Prints the uninstall command.
- **GitOps:** documented Argo CD `Application` and Flux `HelmRelease` examples.
- **Air-gapped:** documented `docker buildx build` + push to private registry + `--set image.repository=`.
- **Uninstall:** `helm uninstall falcon-resource-test -n falcon-resource-test` + delete namespace; nothing left behind.

### Key values

```yaml
collection:
  interval: 15s
discovery:
  namespaces: []          # empty = auto-discover
  extraSelectors: []
persistence:
  enabled: false
  size: 1Gi
  retentionDays: 30
falconApi:
  enabled: false
  existingSecret: ""      # keys: client_id, client_secret
  cloud: autodiscover
prometheus:
  url: ""
  backfillDays: 7
networkPolicy:
  enabled: true
ingress:
  enabled: false
resources:
  requests: { cpu: 10m, memory: 32Mi }
  limits:   { cpu: 100m, memory: 128Mi }
```

## 13. Site integration

- Guide page `se-built/falcon-resource-test.md`, dual-mode per repo standard:
  - **Quick Deploy:** run `assess.sh` (or one `helm install` + port-forward), click Run assessment, export.
  - **Full Lab:** what each panel means, reading the report with a customer, GitOps install, persistence, optional integrations, cleanup.
- Placement under Tooling → SE-Built Tools in `webapp/src/content/manifest.js`.
- Update repo `CLAUDE.md` and `README.md`: the repo now contains one tool with source code under `tools/`.

## 14. Error handling

| Situation | Behavior |
|---|---|
| metrics-server missing / unavailable | Banner + install command; usage = "Not measured"; health/config still work |
| No Falcon found | Empty state with searched namespaces/images and override instructions |
| Missing RBAC | `/setup` names the exact permission; affected panels show "Not measured" |
| API call fails / times out (5s) | Sample skipped, recorded as gap |
| Tool restarts | History lost without persistence (UI states this); running assessment → `interrupted` |
| Disk near `sizeLimit` | Retention tightened automatically; banner shown |
| Falcon API auth fails | Sensor-health check = "Not measured: Falcon API auth failed" |

## 15. Testing

- **Unit:** percentile/avg math, share-of-node, headline, each health check (pass/fail/not-measured), rollup + retention, discovery against fixture manifests for Helm and Operator installs, report rendering golden files.
- **Integration (CI):** kind cluster + metrics-server + stand-in Falcon workloads (pause/busybox images labeled and named like Falcon). Install chart, assert `/api/live` and `/api/setup`, run a short assessment (1m in test mode), export report, verify it's self-contained.
- **Budget gate (CI):** after 5 min in kind, read the tool's own PodMetrics; fail the build if CPU avg > 10m or memory > 48Mi, or image > 25 MB.
- **Live validation (before release):** real cluster running Falcon (DaemonSet + KAC + IAR); compare tool numbers with `kubectl top pods -n falcon-system` (within 10%).

## 16. Future phases

- EKS Fargate / sidecar coverage (in-pod cgroup reads, opt-in `pods/exec`).
- Injected sidecars on regular nodes (opt-in all-namespace watch).
- CPU throttling (opt-in `nodes/proxy`) and KAC admission latency (opt-in `/metrics`).
- Opt-in A/B microbenchmark / load generator.
- Serverless containers (ECS Fargate, ACI, Cloud Run).
