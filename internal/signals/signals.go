package signals

type Type string

const (
	AudioSpike Type = "audio_spike"
	ScoreUp    Type = "score_up"
	ScoreDown  Type = "score_down"
)

type Signal struct {
	Type       Type    `json:"type"`
	Time       float64 `json:"time"`
	Confidence float64 `json:"confidence"`

	Home     int `json:"home,omitempty"`
	Away     int `json:"away,omitempty"`
	PrevHome int `json:"prev_home,omitempty"`
	PrevAway int `json:"prev_away,omitempty"`

	AfterGap bool `json:"after_gap,omitempty"`
}
