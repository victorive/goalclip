package buffer

import (
	"testing"

	"goalclip/internal/ingest"
)

func seg(session int, start float64) ingest.Segment {
	return ingest.Segment{Path: "/nonexistent/seg", Start: start, End: start + 2, Session: session}
}

func fill(b *Buffer, session int, from, to float64) {
	for t := from; t < to; t += 2 {
		b.Add(seg(session, t))
	}
}

func TestWindowCoversRequestedRange(t *testing.T) {
	b := New(300)
	fill(b, 1, 0, 120)
	sel, ok := b.Window(20, 55, 40)
	if !ok {
		t.Fatal("no selection")
	}
	if sel.Segs[0].Start > 20 || sel.Segs[len(sel.Segs)-1].End < 55 {
		t.Errorf("segments %v..%v do not cover 20..55", sel.Segs[0].Start, sel.Segs[len(sel.Segs)-1].End)
	}
	if sel.Start != 20 || sel.End != 55 || sel.Trimmed {
		t.Errorf("window %v..%v trimmed=%v", sel.Start, sel.End, sel.Trimmed)
	}
}

func TestOldSegmentsArePruned(t *testing.T) {
	b := New(60)
	fill(b, 1, 0, 200)
	if _, ok := b.Window(0, 30, 10); ok {
		t.Error("footage older than the retention window should be gone")
	}
	if end := b.LatestEnd(); end != 200 {
		t.Errorf("latest end %v, want 200", end)
	}
}

func TestClipNeverSpansAGap(t *testing.T) {
	b := New(600)
	fill(b, 1, 0, 100)
	fill(b, 2, 130, 200)
	sel, ok := b.Window(75, 115, 95)
	if !ok {
		t.Fatal("no selection")
	}
	if sel.End > 100 {
		t.Errorf("clip end %v crosses the gap", sel.End)
	}
	if !sel.Trimmed {
		t.Error("clip should be marked trimmed")
	}
	for _, s := range sel.Segs {
		if s.Session != 1 {
			t.Errorf("segment from session %d leaked into the clip", s.Session)
		}
	}
}

func TestEmptyBuffer(t *testing.T) {
	b := New(60)
	if _, ok := b.Window(0, 10, 5); ok {
		t.Error("empty buffer has no window")
	}
	if b.LatestEnd() != -1 {
		t.Error("empty buffer latest end should be -1")
	}
}
