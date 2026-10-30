package audio

import (
	"math"
	"math/rand"
	"testing"

	"goalclip/internal/config"
	"goalclip/internal/signals"
)

func noise(r *rand.Rand, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16((r.Float64()*2 - 1) * amp)
	}
	return out
}

func run(t *testing.T, segments []struct {
	seconds int
	amp     float64
}) []signals.Signal {
	t.Helper()
	var got []signals.Signal
	d := New(config.Default().Audio, func(s signals.Signal) { got = append(got, s) })
	r := rand.New(rand.NewSource(1))
	now := 0.0
	for _, seg := range segments {
		for s := 0; s < seg.seconds; s++ {
			d.Feed(now, noise(r, SampleRate, seg.amp))
			now++
		}
	}
	return got
}

func TestDetectsSustainedRoar(t *testing.T) {
	got := run(t, []struct {
		seconds int
		amp     float64
	}{{30, 100}, {12, 8000}, {30, 100}})
	if len(got) != 1 {
		t.Fatalf("want 1 spike, got %d: %+v", len(got), got)
	}
	if math.Abs(got[0].Time-30) > 2 {
		t.Errorf("onset %.2f, want about 30", got[0].Time)
	}
	if got[0].Confidence < 0.9 {
		t.Errorf("confidence %.2f, want high for a 38 dB jump", got[0].Confidence)
	}
}

func TestIgnoresShortBurst(t *testing.T) {
	got := run(t, []struct {
		seconds int
		amp     float64
	}{{30, 100}, {1, 8000}, {30, 100}})
	if len(got) != 0 {
		t.Fatalf("a 1 s burst should not count, got %+v", got)
	}
}

func TestSteadyLoudnessIsNotASpike(t *testing.T) {
	got := run(t, []struct {
		seconds int
		amp     float64
	}{{120, 6000}})
	if len(got) != 0 {
		t.Fatalf("constant loudness should be the baseline, got %+v", got)
	}
}

func TestTwoSeparateRoars(t *testing.T) {
	got := run(t, []struct {
		seconds int
		amp     float64
	}{{30, 100}, {10, 8000}, {40, 100}, {10, 8000}, {20, 100}})
	if len(got) != 2 {
		t.Fatalf("want 2 spikes, got %d: %+v", len(got), got)
	}
}
