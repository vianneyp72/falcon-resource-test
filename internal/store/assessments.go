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
