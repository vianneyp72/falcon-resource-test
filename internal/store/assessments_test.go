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
