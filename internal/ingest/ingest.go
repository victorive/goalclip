package ingest

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goalclip/internal/probe"
)

type Options struct {
	Input          string
	FFmpeg         string
	SegDir         string
	SegSeconds     int
	FrameW, FrameH int
	FrameInterval  float64
	Audio          bool
	FramesOnly     bool
	ReadRate       float64
	Live           bool
	StallTimeout   time.Duration
	BackoffMax     time.Duration
	Log            *slog.Logger
}

type Segment struct {
	Path       string
	Start, End float64
	Session    int
}

type Handlers struct {
	OnFrame   func(t float64, img image.Image)
	OnAudio   func(t float64, pcm []int16)
	OnSegment func(Segment)
	OnSession func(session int, offset float64, resumed bool)
}

type sessionResult struct {
	last     float64
	lastWall time.Time
	frames   int
}

func Run(ctx context.Context, o Options, h Handlers) error {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.SegSeconds <= 0 {
		o.SegSeconds = 2
	}
	if o.FrameInterval <= 0 {
		o.FrameInterval = 1
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = 30 * time.Second
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	offset := 0.0
	backoff := time.Second
	barren := 0
	for session := 1; ; session++ {
		if h.OnSession != nil {
			h.OnSession(session, offset, session > 1)
		}
		began := time.Now()
		res, err := runSession(ctx, o, h, log, session, offset)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !o.Live {
			return err
		}

		if res.frames == 0 {
			barren++
			if barren >= 10 {
				return fmt.Errorf("giving up after %d sessions without data: %v", barren, err)
			}
		} else {
			barren = 0
		}
		if err == nil {
			log.Warn("input ended, restarting (for a recorded file or on-demand URL, use `clipper replay` instead of `watch`)",
				"session", session, "backoff", backoff)
		} else {
			log.Warn("ingest ended, restarting", "session", session, "err", err, "backoff", backoff)
		}
		if time.Since(began) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > o.BackoffMax {
			backoff = o.BackoffMax
		}
		offset = res.last + time.Since(res.lastWall).Seconds()
	}
}

func buildArgs(o Options, session int, listPath string) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if !o.Live && o.ReadRate > 0 {
		a = append(a, "-readrate", strconv.FormatFloat(o.ReadRate, 'f', -1, 64))
	}
	if strings.HasPrefix(o.Input, "http://") || strings.HasPrefix(o.Input, "https://") {
		a = append(a, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5")
	}
	a = append(a, "-i", o.Input)

	if !o.FramesOnly {
		segPattern := filepath.Join(o.SegDir, fmt.Sprintf("s%d_%%06d.ts", session))
		a = append(a,
			"-map", "0:v:0", "-map", "0:a:0?", "-c", "copy",
			"-f", "segment", "-segment_time", strconv.Itoa(o.SegSeconds), "-segment_format", "mpegts",
			"-segment_list", listPath, "-segment_list_type", "csv", "-segment_list_flags", "+live",
			segPattern)
	}

	a = append(a,
		"-map", "0:v:0", "-an",
		"-vf", fmt.Sprintf("fps=%s,scale=%d:%d", strconv.FormatFloat(1/o.FrameInterval, 'f', -1, 64), o.FrameW, o.FrameH),
		"-f", "image2pipe", "-c:v", "png", "pipe:1")

	if o.Audio {
		a = append(a, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:3")
	}
	return a
}

func runSession(ctx context.Context, o Options, h Handlers, log *slog.Logger, session int, offset float64) (sessionResult, error) {
	res := sessionResult{last: offset, lastWall: time.Now()}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	listPath := filepath.Join(o.SegDir, fmt.Sprintf("s%d.csv", session))
	cmd := exec.CommandContext(sctx, o.FFmpeg, buildArgs(o, session, listPath)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr

	var ar, aw *os.File
	if o.Audio {
		ar, aw, err = os.Pipe()
		if err != nil {
			return res, err
		}
		cmd.ExtraFiles = []*os.File{aw}
	}
	if err := cmd.Start(); err != nil {
		if ar != nil {
			ar.Close()
			aw.Close()
		}
		return res, fmt.Errorf("start ffmpeg: %w", err)
	}
	if aw != nil {
		aw.Close()
	}

	var mu sync.Mutex
	touch := func(t float64, frame bool) {
		mu.Lock()
		if t > res.last {
			res.last = t
		}
		res.lastWall = time.Now()
		if frame {
			res.frames++
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		br := bufio.NewReaderSize(stdout, 1<<20)
		defer io.Copy(io.Discard, br)
		for n := 0; ; n++ {
			img, err := png.Decode(br)
			if err != nil {
				return
			}
			t := offset + float64(n)*o.FrameInterval
			touch(t, true)
			if h.OnFrame != nil {
				h.OnFrame(t, img)
			}
		}
	}()

	if o.Audio {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer ar.Close()
			defer io.Copy(io.Discard, ar)
			buf := make([]byte, 8000)
			var total int64
			for {
				n, err := io.ReadFull(ar, buf)
				if n >= 2 {
					count := n / 2
					pcm := make([]int16, count)
					for i := 0; i < count; i++ {
						pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
					}
					t := offset + float64(total)/16000.0
					total += int64(count)
					touch(t, false)
					if h.OnAudio != nil {
						h.OnAudio(t, pcm)
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}

	if o.Live && o.StallTimeout > 0 {
		go func() {
			tk := time.NewTicker(time.Second)
			defer tk.Stop()
			for {
				select {
				case <-sctx.Done():
					return
				case <-tk.C:
					mu.Lock()
					idle := time.Since(res.lastWall)
					mu.Unlock()
					if idle > o.StallTimeout {
						log.Warn("no data from ffmpeg, restarting it", "idle", idle.Round(time.Second))
						cancel()
						return
					}
				}
			}
		}()
	}

	done := make(chan struct{})
	var twg sync.WaitGroup
	twg.Add(1)
	go func() {
		defer twg.Done()
		tailSegments(listPath, o.SegDir, session, offset, done, func(s Segment) {
			if h.OnSegment != nil {
				h.OnSegment(s)
			}
		})
	}()

	wg.Wait()
	close(done)
	twg.Wait()
	waitErr := cmd.Wait()

	mu.Lock()
	out := res
	mu.Unlock()

	if sctx.Err() != nil {
		return out, nil
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if hint := probe.Diagnose(msg); hint != "" {
			msg += "\nhint: " + hint
		}
		return out, fmt.Errorf("ffmpeg: %w: %s", waitErr, msg)
	}
	return out, nil
}

func tailSegments(listPath, dir string, session int, offset float64, done <-chan struct{}, emit func(Segment)) {
	var pos int64
	var partial string

	poll := func() {
		f, err := os.Open(listPath)
		if err != nil {
			return
		}
		defer f.Close()
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return
		}
		pos += int64(len(data))
		partial += string(data)
		for {
			i := strings.IndexByte(partial, '\n')
			if i < 0 {
				return
			}
			line := strings.TrimSpace(partial[:i])
			partial = partial[i+1:]
			parts := strings.Split(line, ",")
			if len(parts) < 3 {
				continue
			}
			start, err1 := strconv.ParseFloat(parts[1], 64)
			end, err2 := strconv.ParseFloat(parts[2], 64)
			if err1 != nil || err2 != nil {
				continue
			}
			emit(Segment{
				Path:    filepath.Join(dir, parts[0]),
				Start:   offset + start,
				End:     offset + end,
				Session: session,
			})
		}
	}

	tk := time.NewTicker(300 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-done:
			poll()
			return
		case <-tk.C:
			poll()
		}
	}
}

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 2000 {
		t.b = t.b[len(t.b)-2000:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
