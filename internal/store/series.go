package store

import (
	"time"

	"github.com/vianneyp72/falcon-cloud-security-labs/tools/falcon-footprint/internal/model"
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
