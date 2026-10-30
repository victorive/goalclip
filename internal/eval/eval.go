package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goalclip/internal/config"
	"goalclip/internal/pipeline"
)

type Truth struct {
	Goals []struct {
		Time float64 `json:"time"`
	} `json:"goals"`
}

type MatchReport struct {
	File       string
	Truth      int
	Detected   int
	TP, FP, FN int
	ErrSum     float64
	Contained  int
}

func (m MatchReport) Precision() float64 { return ratio(m.TP, m.TP+m.FP) }
func (m MatchReport) Recall() float64    { return ratio(m.TP, m.TP+m.FN) }
func (m MatchReport) MeanErr() float64 {
	if m.TP == 0 {
		return 0
	}
	return m.ErrSum / float64(m.TP)
}
func (m MatchReport) ClipCoverage() float64 { return ratio(m.Contained, m.TP) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}

type Options struct {
	Dir       string
	Cfg       *config.Config
	Speed     float64
	Tolerance float64
	Clips     bool
	OutDir    string
	FFmpeg    string
	Tesseract string
	Log       *slog.Logger
	Out       io.Writer
}

type Report struct {
	Matches []MatchReport
	Total   MatchReport
}

var videoExts = map[string]bool{".mp4": true, ".mkv": true, ".ts": true, ".mov": true, ".webm": true}

func Run(ctx context.Context, o Options) (Report, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Tolerance <= 0 {
		o.Tolerance = 25
	}
	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		return Report{}, err
	}

	var rep Report
	rep.Total.File = "TOTAL"
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		truthPath := filepath.Join(o.Dir, base+".goals.json")
		truth, err := loadTruth(truthPath)
		if err != nil {
			fmt.Fprintf(o.Out, "skip %s: %v\n", e.Name(), err)
			continue
		}

		cfg := o.Cfg
		if p := filepath.Join(o.Dir, base+".config.json"); fileExists(p) {
			if cfg, err = config.Load(p); err != nil {
				return rep, err
			}
		}

		fmt.Fprintf(o.Out, "replaying %s (%d goals)...\n", e.Name(), len(truth))
		results, err := pipeline.Run(ctx, pipeline.Options{
			Input:     filepath.Join(o.Dir, e.Name()),
			Cfg:       cfg,
			OutDir:    filepath.Join(o.OutDir, base),
			Name:      base,
			Speed:     o.Speed,
			FFmpeg:    o.FFmpeg,
			Tesseract: o.Tesseract,
			NoClips:   !o.Clips,
			Log:       o.Log,
		})
		if err != nil {
			return rep, fmt.Errorf("%s: %w", e.Name(), err)
		}

		kept := make([]pipeline.Result, 0, len(results))
		for _, r := range results {
			if !r.Retracted && !r.Goal.Review {
				kept = append(kept, r)
			}
		}
		m := Score(truth, kept, o.Tolerance)
		m.File = e.Name()
		rep.Matches = append(rep.Matches, m)
		rep.Total.Truth += m.Truth
		rep.Total.Detected += m.Detected
		rep.Total.TP += m.TP
		rep.Total.FP += m.FP
		rep.Total.FN += m.FN
		rep.Total.ErrSum += m.ErrSum
		rep.Total.Contained += m.Contained
	}
	return rep, nil
}

func Score(truth []float64, results []pipeline.Result, tol float64) MatchReport {
	sorted := make([]float64, len(truth))
	copy(sorted, truth)
	sort.Float64s(sorted)

	m := MatchReport{Truth: len(truth), Detected: len(results)}
	used := make([]bool, len(results))
	for _, tt := range sorted {
		best, bi := math.MaxFloat64, -1
		for i, r := range results {
			if used[i] {
				continue
			}
			if d := math.Abs(r.Goal.EventTime - tt); d <= tol && d < best {
				best, bi = d, i
			}
		}
		if bi < 0 {
			m.FN++
			continue
		}
		used[bi] = true
		m.TP++
		m.ErrSum += best
		if tt >= results[bi].ClipStart && tt <= results[bi].ClipEnd {
			m.Contained++
		}
	}
	for _, u := range used {
		if !u {
			m.FP++
		}
	}
	return m
}

const (
	TargetRecall    = 0.90
	TargetPrecision = 0.85
	TargetCoverage  = 0.95
)

func (r Report) Print(w io.Writer) {
	fmt.Fprintf(w, "\n%-28s %5s %5s %4s %4s %4s %9s %9s %9s\n",
		"match", "goals", "found", "TP", "FP", "FN", "precision", "recall", "mean err")
	rows := append(append([]MatchReport{}, r.Matches...), r.Total)
	for _, m := range rows {
		fmt.Fprintf(w, "%-28s %5d %5d %4d %4d %4d %8.0f%% %8.0f%% %8.1fs\n",
			trunc(m.File, 28), m.Truth, m.Detected, m.TP, m.FP, m.FN,
			100*m.Precision(), 100*m.Recall(), m.MeanErr())
	}
	t := r.Total
	fmt.Fprintf(w, "\nclip contains the goal: %.0f%% of true positives\n", 100*t.ClipCoverage())
	fmt.Fprintf(w, "targets: recall >= %.0f%% [%s]  precision >= %.0f%% [%s]  coverage >= %.0f%% [%s]\n",
		100*TargetRecall, mark(t.Recall() >= TargetRecall),
		100*TargetPrecision, mark(t.Precision() >= TargetPrecision),
		100*TargetCoverage, mark(t.ClipCoverage() >= TargetCoverage))
}

func (r Report) MeetsTargets() bool {
	t := r.Total
	return t.Recall() >= TargetRecall && t.Precision() >= TargetPrecision && t.ClipCoverage() >= TargetCoverage
}

func mark(ok bool) string {
	if ok {
		return "pass"
	}
	return "FAIL"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "~"
}

func loadTruth(path string) ([]float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("missing ground truth: %w", err)
	}
	var t Truth
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]float64, len(t.Goals))
	for i, g := range t.Goals {
		out[i] = g.Time
	}
	return out, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
