package ocr

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goalclip/internal/config"
)

type Score struct{ Home, Away int }

type Reader struct {
	cfg       config.Scoreboard
	tesseract string
	re        *regexp.Regexp

	haveLast  bool
	lastHash  uint64
	lastScore Score
	lastOK    bool
}

func NewReader(cfg config.Scoreboard, tesseractPath string) (*Reader, error) {
	if tesseractPath == "" {
		tesseractPath = "tesseract"
	}
	if _, err := exec.LookPath(tesseractPath); err != nil {
		return nil, fmt.Errorf("tesseract not found (%q): install it or pass --tesseract: %w", tesseractPath, err)
	}
	re, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return nil, fmt.Errorf("scoreboard.pattern: %w", err)
	}
	if re.NumSubexp() < 2 {
		return nil, fmt.Errorf("scoreboard.pattern needs two capture groups (home, away)")
	}
	return &Reader{cfg: cfg, tesseract: tesseractPath, re: re}, nil
}

func (r *Reader) Read(img image.Image) (score Score, ok bool, err error) {
	g := r.crop(img)
	if g == nil {
		return Score{}, false, fmt.Errorf("scoreboard region lies outside the frame (%v)", img.Bounds().Size())
	}
	h := hashGray(g)
	if r.haveLast && h == r.lastHash {
		return r.lastScore, r.lastOK, nil
	}
	text, err := r.runTesseract(r.prepare(g))
	if err != nil {
		return Score{}, false, err
	}
	score, ok = r.parse(text)
	r.haveLast, r.lastHash, r.lastScore, r.lastOK = true, h, score, ok
	return score, ok, nil
}

func (r *Reader) parse(text string) (Score, bool) {
	m := r.re.FindStringSubmatch(text)
	if m == nil {
		return Score{}, false
	}
	h, err1 := strconv.Atoi(m[1])
	a, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || h > 30 || a > 30 {
		return Score{}, false
	}
	return Score{Home: h, Away: a}, true
}

func (r *Reader) crop(img image.Image) *image.Gray {
	b := img.Bounds()
	rect := image.Rect(r.cfg.X, r.cfg.Y, r.cfg.X+r.cfg.W, r.cfg.Y+r.cfg.H).Add(b.Min).Intersect(b)
	if rect.Empty() {
		return nil
	}
	g := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			g.SetGray(x-rect.Min.X, y-rect.Min.Y, color.GrayModel.Convert(img.At(x, y)).(color.Gray))
		}
	}
	return g
}

func hashGray(g *image.Gray) uint64 {
	q := make([]byte, len(g.Pix))
	for i, v := range g.Pix {
		q[i] = v >> 5
	}
	h := fnv.New64a()
	h.Write(q)
	return h.Sum64()
}

func (r *Reader) prepare(g *image.Gray) *image.Gray {
	out := resizeGray(g, r.cfg.Scale)
	for i, v := range out.Pix {
		if r.cfg.Threshold > 0 {
			if int(v) >= r.cfg.Threshold {
				v = 255
			} else {
				v = 0
			}
		}
		if r.cfg.Invert {
			v = 255 - v
		}
		out.Pix[i] = v
	}
	return out
}

func (r *Reader) runTesseract(g *image.Gray) (string, error) {
	var in bytes.Buffer
	if err := png.Encode(&in, g); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.tesseract, "stdin", "stdout",
		"--psm", "7", "-c", "tessedit_char_whitelist=0123456789-:")
	cmd.Stdin = &in
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func resizeGray(src *image.Gray, f int) *image.Gray {
	if f <= 1 {
		return src
	}
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := sw*f, sh*f
	dst := image.NewGray(image.Rect(0, 0, dw, dh))
	px := func(x, y int) float64 {
		if x < 0 {
			x = 0
		}
		if x > sw-1 {
			x = sw - 1
		}
		if y < 0 {
			y = 0
		}
		if y > sh-1 {
			y = sh - 1
		}
		return float64(src.Pix[y*src.Stride+x])
	}
	for y := 0; y < dh; y++ {
		fy := (float64(y)+0.5)/float64(f) - 0.5
		y0 := int(math.Floor(fy))
		wy := fy - float64(y0)
		for x := 0; x < dw; x++ {
			fx := (float64(x)+0.5)/float64(f) - 0.5
			x0 := int(math.Floor(fx))
			wx := fx - float64(x0)
			v := (px(x0, y0)*(1-wx)+px(x0+1, y0)*wx)*(1-wy) +
				(px(x0, y0+1)*(1-wx)+px(x0+1, y0+1)*wx)*wy
			dst.Pix[y*dst.Stride+x] = uint8(v + 0.5)
		}
	}
	return dst
}
