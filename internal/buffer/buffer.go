package buffer

import (
	"os"
	"sync"

	"goalclip/internal/ingest"
)

const gapTolerance = 0.75

type Selection struct {
	Segs       []ingest.Segment
	Start, End float64
	Trimmed    bool
}

type Buffer struct {
	mu     sync.Mutex
	segs   []ingest.Segment
	retain float64
}

func New(retainSeconds float64) *Buffer {
	return &Buffer{retain: retainSeconds}
}

func (b *Buffer) Add(s ingest.Segment) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.segs = append(b.segs, s)
	cut := s.End - b.retain
	i := 0
	for i < len(b.segs) && b.segs[i].End < cut {
		os.Remove(b.segs[i].Path)
		i++
	}
	b.segs = b.segs[i:]
}

func (b *Buffer) LatestEnd() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.segs) == 0 {
		return -1
	}
	return b.segs[len(b.segs)-1].End
}

func (b *Buffer) Window(t0, t1, anchor float64) (Selection, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.segs) == 0 {
		return Selection{}, false
	}

	var runs [][]ingest.Segment
	cur := []ingest.Segment{b.segs[0]}
	for _, s := range b.segs[1:] {
		prev := cur[len(cur)-1]
		if s.Session != prev.Session || s.Start-prev.End > gapTolerance {
			runs = append(runs, cur)
			cur = nil
		}
		cur = append(cur, s)
	}
	runs = append(runs, cur)

	best := -1
	bestOverlap := 0.0
	for i, run := range runs {
		rs, re := run[0].Start, run[len(run)-1].End
		if anchor >= rs && anchor <= re {
			best = i
			break
		}
		overlap := minf(re, t1) - maxf(rs, t0)
		if overlap > bestOverlap {
			best, bestOverlap = i, overlap
		}
	}
	if best < 0 {
		return Selection{}, false
	}

	var picked []ingest.Segment
	for _, s := range runs[best] {
		if s.End > t0 && s.Start < t1 {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		return Selection{}, false
	}
	sel := Selection{
		Segs:  picked,
		Start: maxf(t0, picked[0].Start),
		End:   minf(t1, picked[len(picked)-1].End),
	}
	sel.Trimmed = sel.Start > t0+0.5 || sel.End < t1-0.5
	return sel, true
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
