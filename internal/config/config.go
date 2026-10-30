package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Name       string         `json:"name"`
	Frame      Frame          `json:"frame"`
	Scoreboard *Scoreboard    `json:"scoreboard,omitempty"`
	Auto       AutoScoreboard `json:"auto_scoreboard"`
	Audio      Audio          `json:"audio"`
	Fusion     Fusion         `json:"fusion"`
	Clip       Clip           `json:"clip"`
	Buffer     Buffer         `json:"buffer"`
	Ingest     Ingest         `json:"ingest"`
}

type Frame struct {
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	IntervalSeconds float64 `json:"interval_seconds"`
}

type Scoreboard struct {
	X            int    `json:"x"`
	Y            int    `json:"y"`
	W            int    `json:"w"`
	H            int    `json:"h"`
	Scale        int    `json:"scale"`
	Threshold    int    `json:"threshold"`
	Invert       bool   `json:"invert"`
	Pattern      string `json:"pattern"`
	StableFrames int    `json:"stable_frames"`
}

type AutoScoreboard struct {
	Enabled            bool    `json:"enabled"`
	SampleEverySeconds float64 `json:"sample_every_seconds"`
	MinSamples         int     `json:"min_samples"`
	MaxSamples         int     `json:"max_samples"`
	MinPresence        float64 `json:"min_presence"`
	SearchSeconds      float64 `json:"search_seconds"`
	RemindEverySeconds float64 `json:"remind_every_seconds"`
	RelostSeconds      float64 `json:"relost_seconds"`
	AudioFallback      bool    `json:"audio_fallback"`
}

type Audio struct {
	Enabled           bool    `json:"enabled"`
	ThresholdDB       float64 `json:"threshold_db"`
	BaselineSeconds   float64 `json:"baseline_seconds"`
	FastSeconds       float64 `json:"fast_seconds"`
	SustainSeconds    float64 `json:"sustain_seconds"`
	RefractorySeconds float64 `json:"refractory_seconds"`
}

type Fusion struct {
	ConfirmDelay           float64 `json:"confirm_delay_seconds"`
	AudioWindow            float64 `json:"audio_window_seconds"`
	ScoreLag               float64 `json:"score_lag_seconds"`
	Cooldown               float64 `json:"cooldown_seconds"`
	AudioOnlyMinConfidence float64 `json:"audio_only_min_confidence"`
	RetractWindowSeconds   float64 `json:"retract_window_seconds"`
	ReviewClips            bool    `json:"review_clips"`
	ReviewWaitSeconds      float64 `json:"review_wait_seconds"`
	ReviewMinConfidence    float64 `json:"review_min_confidence"`
}

type Clip struct {
	PreSeconds  float64 `json:"pre_seconds"`
	PostSeconds float64 `json:"post_seconds"`
	Reencode    bool    `json:"reencode"`
}

type Buffer struct {
	RetainSeconds  float64 `json:"retain_seconds"`
	SegmentSeconds int     `json:"segment_seconds"`
}

type Ingest struct {
	StallTimeoutSeconds float64 `json:"stall_timeout_seconds"`
	FreezeSeconds       float64 `json:"freeze_seconds"`
	BackoffMaxSeconds   float64 `json:"backoff_max_seconds"`
}

const DefaultPattern = `(\d{1,2})\s*[-:]\s*(\d{1,2})`

func Default() *Config {
	return &Config{
		Frame: Frame{Width: 1280, Height: 720, IntervalSeconds: 1},
		Auto: AutoScoreboard{
			Enabled:            true,
			SampleEverySeconds: 5,
			MinSamples:         8,
			MaxSamples:         24,
			MinPresence:        0.6,
			SearchSeconds:      1800,
			RemindEverySeconds: 300,
			RelostSeconds:      600,
		},
		Audio: Audio{
			Enabled:           true,
			ThresholdDB:       8,
			BaselineSeconds:   60,
			FastSeconds:       1.5,
			SustainSeconds:    3,
			RefractorySeconds: 20,
		},
		Fusion: Fusion{
			ConfirmDelay:           15,
			AudioWindow:            30,
			ScoreLag:               8,
			Cooldown:               60,
			AudioOnlyMinConfidence: 0.6,
			RetractWindowSeconds:   300,
			ReviewClips:            true,
			ReviewWaitSeconds:      40,
			ReviewMinConfidence:    0.8,
		},
		Clip:   Clip{PreSeconds: 20, PostSeconds: 20},
		Buffer: Buffer{RetainSeconds: 300, SegmentSeconds: 2},
		Ingest: Ingest{StallTimeoutSeconds: 20, FreezeSeconds: 20, BackoffMaxSeconds: 30},
	}
}

func (s *Scoreboard) applyDefaults() {
	if s.Scale == 0 {
		s.Scale = 3
	}
	if s.StableFrames == 0 {
		s.StableFrames = 3
	}
	if s.Pattern == "" {
		s.Pattern = DefaultPattern
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Scoreboard != nil {
		c.Scoreboard.applyDefaults()
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) Validate() error {
	if c.Frame.Width <= 0 || c.Frame.Height <= 0 {
		return fmt.Errorf("frame.width and frame.height must be positive")
	}
	if c.Frame.IntervalSeconds < 0.5 || c.Frame.IntervalSeconds > 10 {
		return fmt.Errorf("frame.interval_seconds must be between 0.5 and 10")
	}
	if c.Fusion.ReviewClips && (c.Fusion.ReviewWaitSeconds <= 0 || c.Fusion.ReviewMinConfidence < 0 || c.Fusion.ReviewMinConfidence > 1) {
		return fmt.Errorf("fusion: review_wait_seconds must be positive and review_min_confidence between 0 and 1")
	}
	if c.Fusion.RetractWindowSeconds < 0 {
		return fmt.Errorf("fusion.retract_window_seconds must not be negative")
	}
	if sb := c.Scoreboard; sb != nil {
		if sb.W <= 0 || sb.H <= 0 {
			return fmt.Errorf("scoreboard.w and scoreboard.h must be positive")
		}
		if sb.X < 0 || sb.Y < 0 || sb.X+sb.W > c.Frame.Width || sb.Y+sb.H > c.Frame.Height {
			return fmt.Errorf("scoreboard region (%d,%d %dx%d) lies outside the %dx%d frame",
				sb.X, sb.Y, sb.W, sb.H, c.Frame.Width, c.Frame.Height)
		}
		if sb.Scale < 1 || sb.Scale > 8 {
			return fmt.Errorf("scoreboard.scale must be between 1 and 8")
		}
		if sb.StableFrames < 1 {
			return fmt.Errorf("scoreboard.stable_frames must be at least 1")
		}
		if sb.Threshold < 0 || sb.Threshold > 255 {
			return fmt.Errorf("scoreboard.threshold must be between 0 and 255")
		}
	}
	if c.Scoreboard == nil && !c.Auto.Enabled && !c.Audio.Enabled {
		return fmt.Errorf("enable at least one detector: auto_scoreboard, a scoreboard region, or audio")
	}
	if a := c.Auto; a.Enabled {
		if a.SampleEverySeconds <= 0 || a.MinSamples < 3 || a.MaxSamples < a.MinSamples ||
			a.MinPresence <= 0 || a.MinPresence > 1 || a.SearchSeconds <= 0 || a.RemindEverySeconds <= 0 || a.RelostSeconds <= 0 {
			return fmt.Errorf("auto_scoreboard: need sample_every_seconds > 0, min_samples >= 3, max_samples >= min_samples, " +
				"0 < min_presence <= 1, and positive search_seconds, remind_every_seconds and relost_seconds")
		}
	}
	if c.Audio.Enabled {
		a := c.Audio
		if a.ThresholdDB <= 0 || a.BaselineSeconds <= 0 || a.FastSeconds <= 0 || a.SustainSeconds <= 0 {
			return fmt.Errorf("audio thresholds and durations must be positive")
		}
	}
	if c.Clip.PreSeconds < 0 || c.Clip.PostSeconds <= 0 {
		return fmt.Errorf("clip.pre_seconds must be >= 0 and clip.post_seconds > 0")
	}
	need := c.Clip.PreSeconds + c.Clip.PostSeconds + c.Fusion.ConfirmDelay + 30
	if c.Buffer.RetainSeconds < need {
		return fmt.Errorf("buffer.retain_seconds must be at least %.0f for these clip and fusion settings", need)
	}
	if c.Buffer.SegmentSeconds < 1 {
		return fmt.Errorf("buffer.segment_seconds must be at least 1")
	}
	return nil
}
