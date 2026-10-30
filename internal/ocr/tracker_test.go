package ocr

import (
	"regexp"
	"testing"

	"goalclip/internal/config"
	"goalclip/internal/signals"
)

func feed(tr *Tracker, start float64, s Score, n int) []signals.Signal {
	var out []signals.Signal
	for i := 0; i < n; i++ {
		out = append(out, tr.Observe(start+float64(i), s, true)...)
	}
	return out
}

func TestGoalIsTimestampedAtFirstStableRead(t *testing.T) {
	tr := NewTracker(3)
	if got := feed(tr, 0, Score{0, 0}, 10); len(got) != 0 {
		t.Fatalf("baseline must not signal: %+v", got)
	}
	got := feed(tr, 46, Score{1, 0}, 5)
	if len(got) != 1 {
		t.Fatalf("want 1 signal, got %+v", got)
	}
	s := got[0]
	if s.Type != signals.ScoreUp || s.Time != 46 || s.Home != 1 || s.Away != 0 || s.PrevHome != 0 {
		t.Errorf("unexpected signal %+v", s)
	}
}

func TestSingleMisreadIsIgnored(t *testing.T) {
	tr := NewTracker(3)
	feed(tr, 0, Score{1, 0}, 5)
	var all []signals.Signal
	all = append(all, tr.Observe(5, Score{7, 0}, true)...)
	all = append(all, feed(tr, 6, Score{1, 0}, 5)...)
	if len(all) != 0 {
		t.Fatalf("a one-frame misread must not signal: %+v", all)
	}
}

func TestUnreadableFramesDoNotBreakARun(t *testing.T) {
	tr := NewTracker(3)
	feed(tr, 0, Score{0, 0}, 5)
	var got []signals.Signal
	got = append(got, tr.Observe(10, Score{0, 1}, true)...)
	got = append(got, tr.Observe(11, Score{}, false)...)
	got = append(got, tr.Observe(12, Score{0, 1}, true)...)
	got = append(got, tr.Observe(13, Score{0, 1}, true)...)
	if len(got) != 1 || got[0].Time != 10 {
		t.Fatalf("want one signal at t=10, got %+v", got)
	}
}

func TestScoreDownAfterVAR(t *testing.T) {
	tr := NewTracker(3)
	feed(tr, 0, Score{1, 0}, 5)
	got := feed(tr, 10, Score{0, 0}, 4)
	if len(got) != 1 || got[0].Type != signals.ScoreDown || got[0].PrevHome != 1 {
		t.Fatalf("want a score_down signal, got %+v", got)
	}
}

func TestImplausibleJumpIsNotAGoal(t *testing.T) {
	tr := NewTracker(3)
	feed(tr, 0, Score{1, 0}, 5)
	if got := feed(tr, 10, Score{9, 0}, 5); len(got) != 0 {
		t.Fatalf("a +8 jump is a misread, got %+v", got)
	}
}

func TestChangeAfterGapIsFlagged(t *testing.T) {
	tr := NewTracker(3)
	feed(tr, 0, Score{0, 0}, 5)
	tr.Gap(100)
	got := feed(tr, 105, Score{1, 0}, 4)
	if len(got) != 1 || !got[0].AfterGap {
		t.Fatalf("want an after-gap signal, got %+v", got)
	}
	got = feed(tr, 500, Score{1, 1}, 4)
	if len(got) != 1 || got[0].AfterGap {
		t.Fatalf("later goal must not be flagged, got %+v", got)
	}
}

func TestParse(t *testing.T) {
	r := &Reader{re: regexp.MustCompile(config.DefaultPattern)}
	cases := []struct {
		in   string
		want Score
		ok   bool
	}{
		{"1-0", Score{1, 0}, true},
		{"2 - 1", Score{2, 1}, true},
		{"12:3", Score{12, 3}, true},
		{"-", Score{}, false},
		{"", Score{}, false},
		{"99-0", Score{}, false},
	}
	for _, c := range cases {
		got, ok := r.parse(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parse(%q) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
