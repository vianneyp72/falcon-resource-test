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
