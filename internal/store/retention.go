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
