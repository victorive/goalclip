package eval

import (
	"testing"

	"goalclip/internal/fusion"
	"goalclip/internal/pipeline"
)

func res(event, start, end float64) pipeline.Result {
	return pipeline.Result{Goal: fusion.Goal{EventTime: event}, ClipStart: start, ClipEnd: end}
}

func TestScoreMatchesOneToOne(t *testing.T) {
	truth := []float64{40, 85, 130}
	got := []pipeline.Result{
		res(41, 21, 61),
		res(86, 66, 106),
		res(400, 380, 420),
	}
	m := Score(truth, got, 25)
	if m.TP != 2 || m.FP != 1 || m.FN != 1 {
		t.Fatalf("TP/FP/FN = %d/%d/%d, want 2/1/1", m.TP, m.FP, m.FN)
	}
	if m.Contained != 2 {
		t.Errorf("contained %d, want 2", m.Contained)
	}
	if got := m.MeanErr(); got != 1 {
		t.Errorf("mean err %.2f, want 1", got)
	}
}

func TestOneDetectionCannotMatchTwoGoals(t *testing.T) {
	m := Score([]float64{100, 110}, []pipeline.Result{res(105, 85, 125)}, 25)
	if m.TP != 1 || m.FN != 1 || m.FP != 0 {
		t.Fatalf("TP/FP/FN = %d/%d/%d, want 1/0/1", m.TP, m.FP, m.FN)
	}
}

func TestClipThatMissesTheGoalIsNotContained(t *testing.T) {
	m := Score([]float64{100}, []pipeline.Result{res(100, 110, 130)}, 25)
	if m.TP != 1 || m.Contained != 0 {
		t.Fatalf("TP=%d contained=%d, want 1 and 0", m.TP, m.Contained)
	}
}
