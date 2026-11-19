# Falcon Resource Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `falcon-resource-test`, a lightweight read-only in-cluster tool that shows live CrowdStrike Falcon CPU/memory/health in a Kubernetes cluster and produces exportable assessment reports.

**Architecture:** One static Go binary (one Deployment, one Helm chart). It polls `metrics.k8s.io` and a few K8s APIs, writes samples to embedded SQLite, computes stats/health checks on request, and serves a React + uPlot UI embedded via `go:embed`. The HTML export is rendered server-side with inline SVG and no JS.

**Tech Stack:** Go (client-go typed clients, `k8s.io/metrics`, `modernc.org/sqlite`), React 19 + Vite + uPlot + Vitest, Helm 3/4, kind, GitHub Actions, cosign.

**Spec:** `docs/design/design.md`

**Contract:** `docs/plan/review-fixes.md` supersedes the code blocks for Tasks 2-8 and records the v1 scope cuts (no persistence or rollups, no Prometheus backfill, no Falcon API). Where a later task disagrees with it, the contract wins.

---

## Plan files (execute in order)

| File | Tasks |
|---|---|
| `01-foundation.md` | 0 Toolchain + module, 1 Model types, 2 Store core, 3 Store series/rollup/retention, 4 Store assessments |
| `02-discovery-analysis.md` | 5 Discovery, 6 Stats, 7 Health checks, 8 Analyze + headline |
| `03-collection.md` | 9 K8s/metrics adapters, 10 Fast tick, 11 Slow tick |
| `04-server.md` | 12 Assessment manager, 13 HTML report, 14 Preflight, 15 HTTP API, 16 main wiring |
| `06-ui.md` | 19 UI scaffold, 20 Live page, 21 Assessments pages, 22 Setup page + embed |
| `07-packaging.md` | 23 Dockerfile, 24 Helm chart, 25 assess.sh, 26 kind e2e + budget gate, 27 CI workflow |
| `08-docs.md` | 28 Labs-site guide (in falcon-cloud-security-labs), 29 README |

## Deliberate deviations from the spec (decided while planning)

1. **Polling instead of informers.** Informer caches are the biggest memory cost in small controllers. We poll on two schedules instead, and hold no object caches:
   - **Fast tick** (`collection.interval`, 15s): pod metrics for Falcon namespaces, node metrics, pod list for Falcon namespaces.
   - **Slow tick** (60s): nodes, DaemonSets, webhooks, eviction events (`fieldSelector=reason=Evicted`).
   - **Discovery:** every 5 min.
   This is lighter than the spec's watch-based design and has the same read-only RBAC.
2. **Typed clients only.** Prod code imports individual typed clients (`core/v1`, `apps/v1`, `admissionregistration/v1`, `authorization/v1`), not `kubernetes.Clientset`. That keeps the binary and image small. Tests use `k8s.io/client-go/kubernetes/fake`.
3. **The image budget is measured compressed** (`docker save | gzip`). That is what a registry stores and pulls.
4. **`GOMEMLIMIT`.** It's set in `main` to 90% of the memory limit, which the Downward API passes in as `MEMORY_LIMIT_BYTES`.
5. **Protobuf for built-in APIs.** Core, apps, and admission clients request protobuf, which makes responses smaller and cheaper to decode. `metrics.k8s.io` stays JSON.
6. **Frozen reports.** When an assessment completes, its full report (`report.Bundle`) is stored as JSON in `assessments.result`. Saved reports survive retention, and exports don't recompute.
7. **Write-time aggregates.** Per-tick component and cluster totals are written alongside raw samples, so every stat is exact and analysis memory stays flat. Raw data only, kept up to 24h (see `review-fixes.md`).
8. **Env-only config.** The binary reads environment variables (set by the chart), not flags.
9. **Reduced RBAC.** Polling only needs `list`, plus `create` on `selfsubjectaccessreviews`. There's no `get`/`watch`, and no `namespaces` access. `TestChartRBACMatchesRequired` (Task 24) enforces this.
10. **No `discovery.extraSelectors` in v1.** Image and label matching already cover Helm, Operator, and mirrored-registry installs. Only `discovery.namespaces` is exposed.

## File map

```

├── go.mod / go.sum
├── cmd/falcon-resource-test/
│   ├── config.go                         env parsing (testable)
│   └── main.go                           wiring, run loops, disk guard
├── internal/model/model.go               Component enum, snapshot payload types
├── internal/store/
│   ├── store.go                          Open, schema, inserts, raw queries
│   ├── series.go                         ComponentTicks, ClusterTicks, EachNode, chart series
│   ├── retention.go                      Expire, PruneAssessments
│   └── assessments.go                    assessment CRUD
├── internal/discover/discover.go         Classify, ComponentForPod, Discover
├── internal/analyze/
│   ├── stats.go                          Percentile, Summarize
│   ├── checks.go                         health checks
│   └── analyze.go                        Input, Report, Analyze, Headline
├── internal/collect/
│   ├── sources.go                        Kube struct, MetricsSource + real impl
│   ├── collector.go                      Collector, Status, FastTick
│   └── slow.go                           SlowTick (nodes, ds, webhooks, evictions)
├── internal/assess/manager.go            Start, History, Refresh, Recover
├── internal/report/
│   ├── report.go                         Bundle, Build, Render (html/template)
│   ├── svg.go                            AreaPath
│   └── report.html.tmpl
├── internal/setup/
│   ├── setup.go                          preflight checks, Required (RBAC source of truth)
│   └── rbac_test.go                      chart RBAC must equal Required
├── internal/web/
│   ├── live.go                           /api/live view model
│   ├── server.go                         routes, handlers, static + SPA fallback
│   ├── embed.go                          //go:embed all:dist
│   └── dist/.gitkeep
├── ui/                                   Vite app (builds into internal/web/dist)
├── chart/                                Helm chart (read-only RBAC, restricted PSS, NetworkPolicy)
├── scripts/assess.sh, scripts/check-ui-size.sh
├── test/chart.sh, test/e2e.sh, test/fake-falcon.yaml
├── Dockerfile, .dockerignore, Makefile
├── .github/workflows/ci.yml
└── README.md
```

## Conventions for every task

- Work dir for Go commands: the repo root. Module path: `github.com/vianneyp72/falcon-resource-test`.
- Timestamps in SQLite are unix seconds (`INTEGER`). In Go they're `time.Time`, converted with `.Unix()` and `time.Unix(ts, 0)`.
- CPU is in **millicores** (`int64`) and memory in **bytes** (`int64`) everywhere.
- Commit after each task, with the attribution trailer `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Never add write verbs to RBAC. If a task seems to need one, stop and ask.
