package ocr

import "goalclip/internal/signals"

type Tracker struct {
	stable int

	have bool
	cur  Score

	cand  Score
	candN int
	candT float64

	gapUntil float64
	hasGap   bool
}

const gapWindow = 120.0

func NewTracker(stableFrames int) *Tracker {
	if stableFrames < 1 {
		stableFrames = 1
	}
	return &Tracker{stable: stableFrames}
}

func (t *Tracker) Ready() bool { return t.have }

func (t *Tracker) Gap(at float64) {
	t.candN = 0
	t.hasGap = true
	t.gapUntil = at + gapWindow
}

func (t *Tracker) Observe(tm float64, s Score, ok bool) []signals.Signal {
	if !ok {
		return nil
	}

	if !t.have {
		if t.candN > 0 && s == t.cand {
			t.candN++
		} else {
			t.cand, t.candN, t.candT = s, 1, tm
		}
		if t.candN >= t.stable {
			t.cur, t.have, t.candN = t.cand, true, 0
		}
		return nil
	}

	if s == t.cur {
		t.candN = 0
		return nil
	}
	if t.candN > 0 && s == t.cand {
		t.candN++
	} else {
		t.cand, t.candN, t.candT = s, 1, tm
	}
	if t.candN < t.stable {
		return nil
	}

	prev := t.cur
	t.cur = t.cand
	t.candN = 0
	dh, da := t.cur.Home-prev.Home, t.cur.Away-prev.Away
	total := dh + da

	base := signals.Signal{
		Time: t.candT, Confidence: 1,
		Home: t.cur.Home, Away: t.cur.Away,
		PrevHome: prev.Home, PrevAway: prev.Away,
	}
	switch {
	case dh >= 0 && da >= 0 && total >= 1 && total <= 3:
		base.Type = signals.ScoreUp
		base.AfterGap = t.hasGap && t.candT <= t.gapUntil
		return []signals.Signal{base}
	case dh <= 0 && da <= 0 && total < 0:
		base.Type = signals.ScoreDown
		return []signals.Signal{base}
	}
	return nil
}
