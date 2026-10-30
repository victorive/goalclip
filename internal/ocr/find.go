package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"goalclip/internal/config"
)

var scoreWord = regexp.MustCompile(`^(\d{1,2})[-:](\d{1,2})$`)

type word struct {
	block, par, line int
	x, y, w, h       int
	conf             float64
	text             string
}

type candidate struct {
	box   image.Rectangle
	score Score
	conf  float64
}

type sample struct {
	t     float64
	img   image.Image
	cands []candidate
}

type Detection struct {
	Scoreboard config.Scoreboard
	Score      Score
	Presence   float64
	Preview    image.Image
}

const minObservations = 6

type Finder struct {
	tesseract      string
	frameW, frameH int
	minPresence    float64
	maxSamples     int
	samples        []sample
}

func NewFinder(tesseractPath string, frameW, frameH int, minPresence float64, maxSamples int) *Finder {
	if tesseractPath == "" {
		tesseractPath = "tesseract"
	}
	return &Finder{tesseract: tesseractPath, frameW: frameW, frameH: frameH, minPresence: minPresence, maxSamples: maxSamples}
}

func CheckTesseract(path string) error {
	if path == "" {
		path = "tesseract"
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("tesseract not found (%q): install it or pass --tesseract: %w", path, err)
	}
	return nil
}

func (f *Finder) Count() int { return len(f.samples) }

func (f *Finder) Reset() { f.samples = nil }

func (f *Finder) AddSample(t float64, img image.Image) error {
	cands, err := findCandidates(img, f.tesseract)
	if err != nil {
		return err
	}
	f.push(sample{t: t, img: img, cands: cands})
	return nil
}

func (f *Finder) push(s sample) {
	f.samples = append(f.samples, s)
	if f.maxSamples > 0 && len(f.samples) > f.maxSamples {
		f.samples = f.samples[len(f.samples)-f.maxSamples:]
	}
}

func (f *Finder) Resolve() (*Detection, error) {
	pick, err := f.pick()
	if err != nil {
		return nil, err
	}

	sb := config.Scoreboard{
		X: pick.region.Min.X, Y: pick.region.Min.Y,
		W: pick.region.Dx(), H: pick.region.Dy(),
		Scale: 3, StableFrames: 3, Pattern: config.DefaultPattern,
	}

	best, bestHits := true, -1
	for _, invert := range []bool{true, false} {
		trial := sb
		trial.Invert = invert
		rd, err := NewReader(trial, f.tesseract)
		if err != nil {
			return nil, err
		}
		hits := 0
		for _, si := range pick.sampleIdx {
			s, ok, err := rd.Read(f.samples[si].img)
			if err == nil && ok && pick.scores[s] {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits = invert, hits
		}
	}
	sb.Invert = best

	return &Detection{
		Scoreboard: sb,
		Score:      pick.common,
		Presence:   pick.presence,
		Preview:    drawBox(f.samples[pick.previewIdx].img, pick.region),
	}, nil
}

type pickResult struct {
	region     image.Rectangle
	scores     map[Score]bool
	common     Score
	presence   float64
	sampleIdx  []int
	previewIdx int
}

type obsEntry struct {
	sample int
	c      candidate
}

type cluster struct {
	anchor image.Rectangle
	obs    []obsEntry
}

func (cl *cluster) accepts(b image.Rectangle) bool {
	h := maxInt(cl.anchor.Dy(), b.Dy())
	dx := absInt(center(cl.anchor).X - center(b).X)
	dy := absInt(center(cl.anchor).Y - center(b).Y)
	return dx <= h*3/2 && dy <= h
}

func (cl *cluster) add(si int, c candidate) {
	for i := range cl.obs {
		if cl.obs[i].sample == si {
			if c.conf > cl.obs[i].c.conf {
				cl.obs[i].c = c
			}
			return
		}
	}
	cl.obs = append(cl.obs, obsEntry{sample: si, c: c})
}

func (f *Finder) pick() (*pickResult, error) {
	n := len(f.samples)
	if n == 0 {
		return nil, errors.New("no samples yet")
	}

	var clusters []*cluster
	for si, s := range f.samples {
		for _, c := range s.cands {
			var hit *cluster
			for _, cl := range clusters {
				if cl.accepts(c.box) {
					hit = cl
					break
				}
			}
			if hit == nil {
				hit = &cluster{anchor: c.box}
				clusters = append(clusters, hit)
			}
			hit.add(si, c)
		}
	}

	var (
		bestCl    *cluster
		bestScore float64
	)
	for _, cl := range clusters {
		sort.Slice(cl.obs, func(i, j int) bool { return cl.obs[i].sample < cl.obs[j].sample })
		presence := float64(len(cl.obs)) / float64(n-cl.obs[0].sample)
		if len(cl.obs) < minObservations || presence < f.minPresence {
			continue
		}
		distinct := map[Score]bool{}
		drops := 0
		var confSum float64
		for i, o := range cl.obs {
			distinct[o.c.score] = true
			confSum += clamp100(o.c.conf)
			if i > 0 {
				p := cl.obs[i-1].c.score
				if o.c.score.Home < p.Home || o.c.score.Away < p.Away {
					drops++
				}
			}
		}
		if len(distinct) > 4 || drops > 1 {
			continue
		}
		q := presence * (confSum / float64(len(cl.obs)) / 100)
		if cy := center(cl.anchor).Y; cy < f.frameH*3/10 || cy > f.frameH*7/10 {
			q *= 1.2
		}
		if q > bestScore {
			bestCl, bestScore = cl, q
		}
	}
	if bestCl == nil {
		return nil, errors.New("no stable score-like text found yet")
	}

	res := &pickResult{scores: map[Score]bool{}, presence: float64(len(bestCl.obs)) / float64(n-bestCl.obs[0].sample)}
	counts := map[Score]int{}
	var x0s, y0s, x1s, y1s []int
	bestConf := -1.0
	for _, o := range bestCl.obs {
		res.scores[o.c.score] = true
		counts[o.c.score]++
		res.sampleIdx = append(res.sampleIdx, o.sample)
		x0s = append(x0s, o.c.box.Min.X)
		y0s = append(y0s, o.c.box.Min.Y)
		x1s = append(x1s, o.c.box.Max.X)
		y1s = append(y1s, o.c.box.Max.Y)
		if o.c.conf > bestConf {
			bestConf, res.previewIdx = o.c.conf, o.sample
		}
	}
	most := -1
	for sc, c := range counts {
		if c > most {
			most, res.common = c, sc
		}
	}

	box := image.Rect(median(x0s), median(y0s), median(x1s), median(y1s))
	res.region = padAndClamp(box, f.frameW, f.frameH)
	return res, nil
}

func padAndClamp(b image.Rectangle, frameW, frameH int) image.Rectangle {
	px := maxInt(6, b.Dx()*8/100)
	py := maxInt(3, b.Dy()*15/100)
	r := image.Rect(b.Min.X-px, b.Min.Y-py, b.Max.X+px, b.Max.Y+py)
	return r.Intersect(image.Rect(0, 0, frameW, frameH))
}

func findCandidates(img image.Image, tess string) ([]candidate, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	full := toGray(img, b)

	type pass struct {
		img        *image.Gray
		scale      int
		offX, offY int
	}
	passes := []pass{
		{full, 1, 0, 0},
		{resizeGray(cropGray(full, image.Rect(0, 0, w, h/4)), 2), 2, 0, 0},
		{resizeGray(cropGray(full, image.Rect(0, h*3/4, w, h)), 2), 2, 0, h * 3 / 4},
	}

	var all []candidate
	for _, p := range passes {
		words, err := runTSV(tess, p.img)
		if err != nil {
			return nil, err
		}
		for i := range words {
			words[i].x = words[i].x/p.scale + p.offX
			words[i].y = words[i].y/p.scale + p.offY
			words[i].w /= p.scale
			words[i].h /= p.scale
		}
		all = append(all, candidatesFromWords(words)...)
	}
	return dedupe(all), nil
}

func runTSV(tess string, g *image.Gray) ([]word, error) {
	var in bytes.Buffer
	if err := png.Encode(&in, g); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tess, "stdin", "stdout", "--psm", "11", "tsv")
	cmd.Stdin = &in
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return parseTSV(out.String()), nil
}

func parseTSV(s string) []word {
	var out []word
	for _, line := range strings.Split(s, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 12 || f[0] != "5" {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		var n [7]int
		ok := true
		for i, col := range []int{2, 3, 4, 6, 7, 8, 9} {
			v, err := strconv.Atoi(f[col])
			if err != nil {
				ok = false
				break
			}
			n[i] = v
		}
		if !ok {
			continue
		}
		conf, _ := strconv.ParseFloat(f[10], 64)
		out = append(out, word{
			block: n[0], par: n[1], line: n[2],
			x: n[3], y: n[4], w: n[5], h: n[6],
			conf: conf, text: text,
		})
	}
	return out
}

func candidatesFromWords(words []word) []candidate {
	lines := map[[3]int][]word{}
	for _, w := range words {
		k := [3]int{w.block, w.par, w.line}
		lines[k] = append(lines[k], w)
	}

	var out []candidate
	for _, ws := range lines {
		sort.Slice(ws, func(i, j int) bool { return ws[i].x < ws[j].x })
		for i := range ws {
			for j := i; j < len(ws) && j < i+3; j++ {
				seg := ws[i : j+1]
				if !closeTogether(seg) {
					break
				}
				m := scoreWord.FindStringSubmatch(normalise(joinText(seg)))
				if m == nil {
					continue
				}
				home, _ := strconv.Atoi(m[1])
				away, _ := strconv.Atoi(m[2])
				if home > 30 || away > 30 {
					continue
				}
				box := image.Rect(seg[0].x, seg[0].y, seg[0].x+seg[0].w, seg[0].y+seg[0].h)
				var conf float64
				for _, s := range seg {
					box = box.Union(image.Rect(s.x, s.y, s.x+s.w, s.y+s.h))
					conf += s.conf
				}
				out = append(out, candidate{box: box, score: Score{home, away}, conf: conf / float64(len(seg))})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].box.Min.Y != out[j].box.Min.Y {
			return out[i].box.Min.Y < out[j].box.Min.Y
		}
		return out[i].box.Min.X < out[j].box.Min.X
	})
	return out
}

func closeTogether(seg []word) bool {
	maxH := 0
	for _, s := range seg {
		maxH = maxInt(maxH, s.h)
	}
	for k := 0; k+1 < len(seg); k++ {
		gap := seg[k+1].x - (seg[k].x + seg[k].w)
		if gap > maxH*5/2 {
			return false
		}
	}
	return true
}

func joinText(seg []word) string {
	var sb strings.Builder
	for _, s := range seg {
		sb.WriteString(s.text)
	}
	return sb.String()
}

var dashes = strings.NewReplacer("–", "-", "—", "-", "−", "-", "‑", "-", " ", "")

func normalise(s string) string {
	return strings.Trim(dashes.Replace(s), ".,;'\"`")
}

func dedupe(in []candidate) []candidate {
	var out []candidate
	for _, c := range in {
		merged := false
		for i := range out {
			if out[i].score == c.score && out[i].box.Overlaps(c.box) {
				if c.conf > out[i].conf {
					out[i] = c
				}
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, c)
		}
	}
	return out
}

func toGray(img image.Image, b image.Rectangle) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g.SetGray(x-b.Min.X, y-b.Min.Y, color.GrayModel.Convert(img.At(x, y)).(color.Gray))
		}
	}
	return g
}

func cropGray(g *image.Gray, r image.Rectangle) *image.Gray {
	r = r.Intersect(g.Bounds())
	out := image.NewGray(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+r.Dx()], g.Pix[(r.Min.Y+y)*g.Stride+r.Min.X:])
	}
	return out
}

func drawBox(img image.Image, r image.Rectangle) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, b.Min, draw.Src)
	red := color.RGBA{R: 255, A: 255}
	set := func(x, y int) {
		if image.Pt(x, y).In(b) {
			dst.SetRGBA(x, y, red)
		}
	}
	const thick = 3
	for t := 0; t < thick; t++ {
		for x := r.Min.X - thick; x < r.Max.X+thick; x++ {
			set(x, r.Min.Y-1-t)
			set(x, r.Max.Y+t)
		}
		for y := r.Min.Y - thick; y < r.Max.Y+thick; y++ {
			set(r.Min.X-1-t, y)
			set(r.Max.X+t, y)
		}
	}
	return dst
}

func center(r image.Rectangle) image.Point {
	return image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2)
}

func median(v []int) int {
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func clamp100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
