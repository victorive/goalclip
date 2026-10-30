package audio

import (
	"math"

	"goalclip/internal/config"
	"goalclip/internal/signals"
)

const (
	SampleRate    = 16000
	windowSec     = 0.25
	windowSamples = 4000
	warmupSec     = 10.0
)

type state int

const (
	idle state = iota
	active
	decay
)

type Detector struct {
	cfg  config.Audio
	emit func(signals.Signal)

	sumSq float64
	cnt   int
	winT0 float64

	fast  []float64
	fastN int

	seen     int
	baseline float64

	st       state
	crossT   float64
	onset    float64
	peak     float64
	lastEmit float64
}

func New(cfg config.Audio, emit func(signals.Signal)) *Detector {
	n := int(math.Round(cfg.FastSeconds / windowSec))
	if n < 1 {
		n = 1
	}
	return &Detector{cfg: cfg, emit: emit, fastN: n, lastEmit: -1e9}
}

func (d *Detector) Reset() {
	d.sumSq, d.cnt = 0, 0
	d.fast = d.fast[:0]
	d.st = idle
}

func (d *Detector) Feed(t float64, pcm []int16) {
	for i, s := range pcm {
		if d.cnt == 0 {
			d.winT0 = t + float64(i)/SampleRate
		}
		v := float64(s)
		d.sumSq += v * v
		d.cnt++
		if d.cnt == windowSamples {
			rms := math.Sqrt(d.sumSq/float64(d.cnt)) / 32768.0
			db := 20 * math.Log10(math.Max(rms, 1e-5))
			d.window(d.winT0+windowSec, db)
			d.sumSq, d.cnt = 0, 0
		}
	}
}

func (d *Detector) window(tEnd, db float64) {
	d.fast = append(d.fast, db)
	if len(d.fast) > d.fastN {
		d.fast = d.fast[1:]
	}
	fast := mean(d.fast)

	d.seen++
	if d.seen == 1 {
		d.baseline = db
		return
	}
	warm := float64(d.seen)*windowSec < warmupSec

	T := d.cfg.ThresholdDB
	delta := fast - d.baseline

	frozen := d.st == active || (d.st == decay && tEnd-d.lastEmit < 45)
	if !frozen {
		alpha := windowSec / d.cfg.BaselineSeconds
		if warm {
			alpha = 0.05
		}
		d.baseline += alpha * (fast - d.baseline)
	}
	if warm {
		return
	}

	switch d.st {
	case idle:
		if delta >= T && tEnd-d.lastEmit >= d.cfg.RefractorySeconds {
			d.st = active
			d.crossT = tEnd
			d.onset = tEnd - d.cfg.FastSeconds/2
			d.peak = delta
		}
	case active:
		if delta > d.peak {
			d.peak = delta
		}
		if delta < T/2 {
			d.st = idle
			return
		}
		if tEnd-d.crossT >= d.cfg.SustainSeconds {
			conf := clamp(0.3+0.7*(d.peak-T)/(2*T), 0.3, 1)
			d.emit(signals.Signal{Type: signals.AudioSpike, Time: d.onset, Confidence: conf})
			d.lastEmit = tEnd
			d.st = decay
		}
	case decay:
		if delta < T/2 {
			d.st = idle
		}
	}
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func clamp(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
}
