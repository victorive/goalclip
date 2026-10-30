package ocr

import (
	"image"
	"testing"
)

const sampleTSV = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
	"5\t1\t1\t1\t1\t1\t40\t6\t96\t55\t96.558617\tARS\n" +
	"5\t1\t1\t1\t1\t2\t160\t29\t22\t32\t89.209862\t1\n" +
	"5\t1\t1\t1\t1\t3\t203\t45\t14\t6\t89.209862\t-\n" +
	"5\t1\t1\t1\t1\t4\t243\t28\t20\t33\t92.511024\t0\n" +
	"5\t1\t1\t1\t1\t5\t282\t28\t94\t33\t96.673111\tCHE\n" +
	"5\t1\t2\t1\t1\t1\t1024\t523\t12\t7\t0.000000\tS\n"

func TestParseTSVKeepsWordRows(t *testing.T) {
	words := parseTSV(sampleTSV)
	if len(words) != 6 {
		t.Fatalf("want 6 words, got %d", len(words))
	}
	if words[1].text != "1" || words[1].x != 160 || words[1].w != 22 {
		t.Errorf("unexpected word %+v", words[1])
	}
}

func TestScoreSplitAcrossWordsIsFound(t *testing.T) {
	cands := candidatesFromWords(parseTSV(sampleTSV))
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %+v", cands)
	}
	c := cands[0]
	if c.score != (Score{1, 0}) {
		t.Errorf("score %v, want 1-0", c.score)
	}
	if c.box.Min.X != 160 || c.box.Max.X != 263 || c.box.Min.Y != 28 {
		t.Errorf("box %v", c.box)
	}
}

func TestSingleWordScoreAndUnicodeDash(t *testing.T) {
	words := []word{{1, 1, 1, 100, 50, 60, 30, 90, "2—1"}}
	cands := candidatesFromWords(words)
	if len(cands) != 1 || cands[0].score != (Score{2, 1}) {
		t.Fatalf("got %+v", cands)
	}
}

func TestFarApartNumbersAreNotAScore(t *testing.T) {
	words := []word{
		{1, 1, 1, 100, 50, 20, 30, 90, "1"},
		{1, 1, 1, 130, 50, 20, 30, 90, "-"},
		{1, 1, 1, 600, 50, 20, 30, 90, "0"},
	}
	if got := candidatesFromWords(words); len(got) != 0 {
		t.Fatalf("distant words must not be joined: %+v", got)
	}
}

func TestImplausibleNumbersRejected(t *testing.T) {
	words := []word{{1, 1, 1, 100, 50, 60, 30, 90, "45-12"}}
	if got := candidatesFromWords(words); len(got) != 0 {
		t.Fatalf("a match clock reading is not a score: %+v", got)
	}
}

func cand(x, y, w, h int, s Score, conf float64) candidate {
	return candidate{box: image.Rect(x, y, x+w, y+h), score: s, conf: conf}
}

func finderWith(samples ...[]candidate) *Finder {
	f := NewFinder("tesseract", 1280, 720, 0.6, 0)
	for i, cs := range samples {
		f.samples = append(f.samples, sample{t: float64(i * 5), cands: cs})
	}
	return f
}

func TestStableBugBeatsChangingClock(t *testing.T) {
	var samples [][]candidate
	for i := 0; i < 10; i++ {
		bug := cand(160, 28, 103, 32, Score{0, 0}, 90)
		if i >= 6 {
			bug = cand(160, 28, 103, 32, Score{1, 0}, 90)
		}
		clock := cand(600, 30, 90, 32, Score{i + 1, 10 + i}, 95)
		samples = append(samples, []candidate{bug, clock})
	}
	p, err := finderWith(samples...).pick()
	if err != nil {
		t.Fatal(err)
	}
	if p.region.Min.X > 160 || p.region.Max.X < 263 || p.region.Max.X > 300 {
		t.Errorf("picked region %v, want the bug near x=160..263", p.region)
	}
	if !p.scores[Score{0, 0}] || !p.scores[Score{1, 0}] {
		t.Errorf("scores %v", p.scores)
	}
}

func TestOnlyAChangingClockMeansNoBug(t *testing.T) {
	var samples [][]candidate
	for i := 0; i < 10; i++ {
		samples = append(samples, []candidate{cand(600, 30, 90, 32, Score{i + 1, 10 + i}, 95)})
	}
	if _, err := finderWith(samples...).pick(); err == nil {
		t.Fatal("a value that changes every sample must not be accepted")
	}
}

func TestRareSightingsAreNotEnough(t *testing.T) {
	samples := make([][]candidate, 10)
	samples[2] = []candidate{cand(160, 28, 103, 32, Score{0, 0}, 90)}
	samples[7] = []candidate{cand(160, 28, 103, 32, Score{0, 0}, 90)}
	if _, err := finderWith(samples...).pick(); err == nil {
		t.Fatal("two sightings in ten samples should not be enough")
	}
}

func TestRegionIsClampedToTheFrame(t *testing.T) {
	r := padAndClamp(image.Rect(2, 1, 100, 30), 1280, 720)
	if r.Min.X < 0 || r.Min.Y < 0 {
		t.Errorf("region %v leaves the frame", r)
	}
	r = padAndClamp(image.Rect(1200, 690, 1278, 719), 1280, 720)
	if r.Max.X > 1280 || r.Max.Y > 720 {
		t.Errorf("region %v leaves the frame", r)
	}
}

func TestScoreboardThatAppearsLateIsFound(t *testing.T) {
	samples := make([][]candidate, 12)
	for i := 0; i < 8; i++ {
		samples = append(samples, []candidate{cand(160, 28, 103, 32, Score{0, 0}, 90)})
	}
	p, err := finderWith(samples...).pick()
	if err != nil {
		t.Fatalf("a scoreboard that shows up after the pre-match should be found: %v", err)
	}
	if p.presence < 0.99 {
		t.Errorf("presence %.2f should be measured from its first sighting", p.presence)
	}
}

func TestBriefGraphicIsNotAScoreboard(t *testing.T) {
	samples := make([][]candidate, 12)
	for i := 0; i < 4; i++ {
		samples = append(samples, []candidate{cand(160, 28, 103, 32, Score{2, 1}, 90)})
	}
	if _, err := finderWith(samples...).pick(); err == nil {
		t.Fatal("a graphic seen in only a few samples, such as a previous result, must not be accepted")
	}
}

func TestWindowKeepsOnlyTheNewestSamples(t *testing.T) {
	f := NewFinder("tesseract", 1280, 720, 0.6, 5)
	for i := 0; i < 8; i++ {
		f.push(sample{t: float64(i)})
	}
	if f.Count() != 5 || f.samples[0].t != 3 {
		t.Fatalf("count %d, first t %v; want the 5 newest samples", f.Count(), f.samples[0].t)
	}
}
