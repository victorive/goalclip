# goalclip

`goalclip` is a Go command-line tool that uses [FFmpeg](https://ffmpeg.org/) to read and cut video, [Tesseract OCR](https://github.com/tesseract-ocr/tesseract) to read the on-screen scoreboard, and audio analysis to help estimate when a goal happened. It finds the scoreboard automatically, maintains a five-minute rolling video buffer, and saves an MP4 plus JSON metadata for detected events.

It works with local recordings and accessible live streams.

## Contents

- [goalclip](#goalclip)
  - [Contents](#contents)
  - [Features](#features)
  - [How it works](#how-it-works)
  - [Prerequisites](#prerequisites)
    - [Install dependencies](#install-dependencies)
    - [Verify installation](#verify-installation)
  - [Installation](#installation)
  - [Quick start](#quick-start)
  - [Command reference](#command-reference)
    - [Flags](#flags)
  - [Supported inputs](#supported-inputs)
    - [Input-check example](#input-check-example)
    - [Public example sources](#public-example-sources)
  - [Configuration](#configuration)
    - [Late scoreboard appearance](#late-scoreboard-appearance)
    - [Manual scoreboard calibration](#manual-scoreboard-calibration)
  - [VAR, offside, and reversed goals](#var-offside-and-reversed-goals)
  - [Output](#output)
  - [Speed and performance](#speed-and-performance)
  - [Live streams](#live-streams)
    - [Direct stream URL](#direct-stream-url)
    - [OBS with MediaMTX](#obs-with-mediamtx)
  - [Testing](#testing)
    - [Unit tests](#unit-tests)
    - [Local replay test](#local-replay-test)
    - [URL replay test](#url-replay-test)
    - [Simulated live-stream test](#simulated-live-stream-test)
    - [Accuracy evaluation](#accuracy-evaluation)
  - [Troubleshooting](#troubleshooting)
    - [Input errors](#input-errors)
    - [Runtime issues](#runtime-issues)
  - [Limitations](#limitations)
  - [Contributing](#contributing)
  - [License](#license)

## Features

- **Automatic scoreboard discovery:** Keeps searching if the scoreboard only appears after pre-match coverage.
- **Score and audio detection:** A score change identifies a potential goal; crowd noise helps estimate the event time.
- **Pre-goal footage:** A five-minute rolling buffer lets clips include the build-up to a goal.
- **VAR and offside awareness:** Quick score reversals cancel a candidate; later reversals mark saved clips as `_retracted`; strong roars without a score change can produce `_review` clips.
- **Live and recorded video:** Supports local files, direct video URLs, and FFmpeg-supported live-stream protocols.
- **Detection metadata:** Saves a JSON file beside each clip with the score, confidence, timing, and detection signals.

## How it works

1. **Ingest:** FFmpeg reads the input and produces video segments, still frames, and audio.
2. **Find the scoreboard:** Tesseract looks for score-like text (for example, `1-0`) that stays in one position and changes infrequently. The match clock is ignored. After discovery, only the scoreboard region is read, once per second by default.
3. **Track the score:** A score is accepted only after several consistent OCR readings.
4. **Analyse audio:** The audio detector looks for sustained increases above recent crowd-noise levels. A roar can also come from a near miss or jeering, so audio supports score detection rather than replacing it.
5. **Confirm the event:** After a score increase, `goalclip` waits 15 seconds by default to catch a quick reversal. A nearby crowd roar helps estimate the actual goal time when the scoreboard updates late.
6. **Create the clip:** FFmpeg joins buffered video segments around the event into an MP4. A JSON metadata file is written beside it.

Timing is based on the input video's timeline, not the computer's clock. Replaying a recording at eight times speed therefore preserves the same match-time timestamps as a real-time run.

## Prerequisites

Before building `goalclip`, make sure the following dependencies are installed:

- **Go 1.26 or later**
- **FFmpeg 5.0 or later** (includes `ffprobe`)
- **Tesseract 4 or later** (not required when using `--audio-only`)
- **Linux or macOS** (Windows has not been tested)

### Install dependencies

**macOS (Homebrew)**

```bash
brew install go ffmpeg tesseract
```

For additional Tesseract language support:

```bash
brew install tesseract-lang
```

**Ubuntu / Debian**

```bash
sudo apt update
sudo apt install golang-go ffmpeg tesseract-ocr
```

**Fedora**

```bash
sudo dnf install golang ffmpeg tesseract
```

### Verify installation

Confirm that the required tools are available:

```bash
go version
ffmpeg -version
ffprobe -version
tesseract --version
```

Make sure the installed versions meet the minimum requirements listed above. If your package manager provides an older version, install a newer release from the official sources:

- [Go](https://go.dev/dl/)
- [FFmpeg](https://ffmpeg.org/download.html)
- [Tesseract](https://github.com/tesseract-ocr/tesseract)

## Installation

```bash
git clone https://github.com/victorive/goalclip.git
cd goalclip
go build -o clipper ./cmd/clipper
```

## Quick start

Check an input before processing it:

```bash
./clipper check match.mp4
```

Replay a recording at eight times normal speed:

```bash
./clipper replay match.mp4 --speed 8
```

Watch a live stream:

```bash
./clipper watch rtmp://localhost:1935/live/match
```

Check whether the scoreboard is found correctly. This saves a preview with the detected area outlined in red:

```bash
./clipper calibrate match.mp4
```

Open `scoreboard-detected.png` in the run's output directory and confirm that the box covers the score digits.

## Command reference

| Command | Syntax | Purpose |
|---|---|---|
| `watch` | `clipper watch <url> [flags]` | Monitor a live stream; restart if it drops. |
| `replay` | `clipper replay <file\|url> [flags]` | Process a recorded file or on-demand URL. |
| `check` | `clipper check <file\|url>` | Test whether an input can be read and show likely fixes. |
| `calibrate` | `clipper calibrate <file\|url> [flags]` | Find the scoreboard and show its position. |
| `clip` | `clipper clip <file\|url> --at 00:42:10 [--pre 20] [--post 15] [flags]` | Create a clip around a specified time. |
| `eval` | `clipper eval <dir> [flags]` | Measure accuracy against labelled goal times. |
| `labels` | `clipper labels <Labels-v2.json> [--half 1]` | Convert SoccerNet labels into goal-time data. |

### Flags

| Flag | Commands | Description |
|---|---|---|
| `--config <file>` | `watch`, `replay`, `calibrate`, `eval` | Load optional JSON settings; see [Configuration](#configuration). |
| `--out <dir>` | `watch`, `replay`, `calibrate`, `clip` | Output directory. Each run gets its own folder; default: `./clips`. |
| `--speed <n>` | `replay` | `1` = real time, `8` = eight times faster, `0` = as fast as possible. |
| `--json` | `watch`, `replay` | Print one JSON event per line instead of text. |
| `--audio-only` | `watch`, `replay` | Skip scoreboard OCR and use crowd noise only; less accurate. |
| `--reencode` | `watch`, `replay`, `clip` | Re-encode for exact clip boundaries instead of copying segments. |
| `--name <text>` | `watch`, `replay` | Name for the run folder and clip files. |
| `--ffmpeg <path>` | All | Path to the FFmpeg executable. |
| `--tesseract <path>` | All | Path to the Tesseract executable. |
| `--ffprobe <path>` | `check` | Path to the FFprobe executable. |
| `--debug` | All | Print detailed logs. |

Use `watch` for live streams and `replay` for recordings or on-demand video. When a `watch` input ends, the command assumes the stream dropped and restarts it. A recording ends normally, so it should use `replay`. Press `Ctrl-C` to stop; any goal still awaiting confirmation is clipped before exit.

## Supported inputs

`goalclip` can read anything FFmpeg can open, provided the address points directly to the video and access is not blocked.

| Input | Example | Command |
|---|---|---|
| Local file | `match.mp4`, `/videos/match.mkv` | `replay` |
| Recorded HTTP(S) video | `https://archive.org/download/<item>/<file>.mp4` | `replay` |
| Live HLS | `https://example.com/live/index.m3u8` | `watch` |
| On-demand HLS | `https://example.com/match/index.m3u8` | `replay` |
| RTMP | `rtmp://localhost:1935/live/match` | `watch` |
| RTSP | `rtsp://localhost:8554/match` | `watch` |
| SRT | `srt://host:9000?mode=caller` | `watch` (FFmpeg must include SRT support) |
| UDP / MPEG-TS | `udp://@:1234` | `watch` |

A video track is required; audio is optional. Crowd-noise detection switches off automatically if no audio track is present.

**Not supported:** web-page URLs (use the direct video URL), paid or login-protected streams, and expired links containing temporary access tokens. Bypassing login restrictions or copy protection is not supported. For some public live pages, [Streamlink](https://streamlink.github.io/) can print a direct stream URL:

```bash
streamlink --stream-url <page> best
```

Use Streamlink only for streams you have the right to access. It has not been tested with `goalclip`.

### Input-check example

```bash
./clipper check "https://archive.org/download/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2/2017.03.08%20-%20UCL-R16-G2%20Barcelona%20PSG%206-1.mp4"
```

Example output:

```text
Input:  https://archive.org/download/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2/2017.03.08%20-%20UCL-R16-G2%20Barcelona%20PSG%206-1.mp4
Format: mov,mp4,m4a,3gp,3g2,mj2
Video:  h264, 854x480
Audio:  aac, 48000 Hz
Length: 1:52:47 (recorded, so use `clipper replay`)

OK: this input can be read.
```

If an input cannot be read, the output ends with a `hint:` line. `replay` performs the same check before starting.

### Public example sources

- **Recorded match:** [Barcelona 6–1 Paris Saint-Germain, 8 March 2017](https://archive.org/details/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2). One direct MP4 is `https://archive.org/download/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2/2017.03.08%20-%20UCL-R16-G2%20Barcelona%20PSG%206-1.mp4`.
- **Non-football test streams:** `https://ireplay.tv/test/blender.m3u8` (always on) and `https://demo.unified-streaming.com/k8s/features/stable/video/tears-of-steel/tears-of-steel.mp4/.m3u8` (on-demand). Neither has a scoreboard, so no normal goal clips are expected. Use `--audio-only` to test sound-triggered behaviour.

Public test streams can disappear; run `clipper check` first. See [Live streams](#live-streams) for live setup.

## Configuration

A settings file is optional. Copy `configs/example.json`, edit it, and pass it with `--config`:

```bash
cp configs/example.json config.json
./clipper replay match.mp4 --config config.json
```

Unknown keys are rejected so configuration typos are caught.

| Section | Controls |
|---|---|
| `frame` | Analysis picture size and `interval_seconds` (default: 1 second). |
| `auto_scoreboard` | Search cadence, sample count, `search_seconds`, reminders, and optional `audio_fallback`. |
| `scoreboard` | Manual scoreboard position. When present, automatic search is skipped; see `configs/manual-scoreboard.json`. |
| `audio` | Crowd-noise detection; `threshold_db` is the rise above recent audio level needed for a roar. |
| `fusion` | `confirm_delay_seconds`, `retract_window_seconds`, `review_clips`, `score_lag_seconds`, and `cooldown_seconds`. |
| `clip` | `pre_seconds`, `post_seconds`, and `reencode`. |
| `buffer` | `retain_seconds` (default: 300) and `segment_seconds` (default: 2). |
| `ingest` | Stall timeout, frozen-picture timeout, and restart delay. |

### Late scoreboard appearance

Broadcasts may start with pre-match coverage and show the scoreboard only near kick-off.

- **Replay:** Before playback, samples are taken every 10 seconds for up to `search_seconds` (default: 1,800 seconds / 30 minutes). If the scoreboard is not found, searching continues as the video plays.
- **Watch:** Search starts in the background and slows down after a complete unsuccessful sweep. Score detection usually begins about a minute after the scoreboard appears.
- **Before detection:** No clips are made until the scoreboard is found. This prevents walk-out noise from creating false goals, but goals before detection can be missed.
- **Reminder:** A message is printed every `remind_every_seconds` (default: 300).
- **Audio fallback:** Run with `--audio-only` or set `"auto_scoreboard": {"audio_fallback": true}` to allow sound-only detection while the scoreboard is unavailable.

### Manual scoreboard calibration

Run `clipper calibrate` if automatic search selects the wrong text or finds nothing. It prints a ready-to-use `scoreboard` configuration section. You can also measure the score box in a 1280 × 720 image.

| Field | Meaning |
|---|---|
| `x`, `y`, `w`, `h` | Position and dimensions of the score-digit box. |
| `invert` | Set to `true` for light text on a dark background. |
| `threshold` | `0` disables thresholding; otherwise pixels are forced black or white at this brightness (try `128` if noisy). |
| `scale` | Enlargement before OCR (default: `3`). |
| `pattern` | Home/away score groups; default accepts `2-1` and `2 : 1`. |
| `stable_frames` | Identical readings required to accept a score (default: `3`). |

## VAR, offside, and reversed goals

`goalclip` preserves footage for review. Depending on when the score changes, it cancels a candidate or saves and labels the clip.

| Event | Behaviour | Output |
|---|---|---|
| Score rises, then drops within `confirm_delay_seconds` (default: 15). | Cancels before clipping. | No clip. |
| Score rises, a clip is saved, then the score drops within `retract_window_seconds` (default: 300). | Keeps and marks the clip as retracted. | `*_retracted.mp4` and `*_retracted.json` |
| Strong crowd roar but no score change. | Saves a clip for manual review. | `*_review.mp4` and `*_review.json` |
| Score rises and remains up. | Saves a normal goal clip. | For example, `*_1-0.mp4` |

Retracted JSON contains `"retracted": true` and `retract_reason`. The log prints a message such as `GOAL 1-0 RETRACTED`; with `--json`, a `retracted` event is emitted.

A review clip is created when a strong roar reaches `review_min_confidence` (default: `0.8`) and no score change follows within `review_wait_seconds` (default: `40`). Review events are separated by `cooldown_seconds` and are only enabled after scoreboard detection. Near misses and big chances can also trigger them, so false alarms are expected. Reduce them by increasing `review_min_confidence` or setting `"review_clips": false`.

`clipper eval` excludes retracted and review clips from its counts. Detection depends on broadcast evidence: a VAR graphic alone is not enough; a quiet disallowed goal with no score change can be missed; and a reversal after the retraction window is not linked to the original goal.

To wait longer before confirming a goal and allow a longer retraction period, increase these settings. Clips will be produced later:

```json
{
  "fusion": {
    "confirm_delay_seconds": 60,
    "retract_window_seconds": 600
  }
}
```

## Output

Each run gets a separate directory under `--out` (default: `./clips`):

```text
clips/
└── 2026-10-28_153012_replay/
    ├── scoreboard-detected.png
    ├── replay_003m40s_1-0.mp4
    ├── replay_003m40s_1-0.json
    ├── replay_052m10s_2-1.mp4
    └── replay_052m10s_2-1.json
```

The JSON beside each clip records the score, confidence, detection reason and signals, exact clip start/end, and flags such as `during_gap`, `trimmed`, and `retracted`. Review and retraction suffixes are described above.

By default, clips copy complete two-second video segments. This is fast, but a clip may start up to two seconds before the requested time. `--reencode` gives exact start and end points at the cost of longer processing. A run that finds no goals and no scoreboard does not create an output directory.

## Speed and performance

`replay` accepts `--speed`; `watch` always runs in real time.

| Value | Behaviour |
|---|---|
| `1` | Real time (default) |
| `8` | Eight times faster |
| `0` | As fast as the computer allows |

```bash
./clipper replay match.mp4 --speed 8
./clipper replay match.mp4 --speed 0
```

Speed depends on frame decoding, Tesseract OCR, and (for URLs) network throughput. If `--speed 0` is still too slow:

1. **Analyse fewer frames:** Set `frame.interval_seconds` to `2`. This roughly halves the analysis work but reduces timing precision and can delay score-change detection; audio timing is unaffected.
   ```json
   { "frame": { "interval_seconds": 2 } }
   ```
2. **Shorten the input:** Cut the recording to the section you need, or stop with `Ctrl-C`.
3. **Set the scoreboard manually:** This skips automatic search, which can save time when evaluating many files.
4. **Use `--audio-only`:** Skips OCR and is fastest, but least accurate.
5. **Use a fast disk and avoid `--reencode`:** Copying segments is faster than re-encoding.

## Live streams

Use a direct stream address from a source you have the right to use, or a feed you produce yourself.

### Direct stream URL

```bash
./clipper check "https://example.com/live/match/index.m3u8"
./clipper watch "https://example.com/live/match/index.m3u8" --out ./clips
```

Use a stable URL: adverts can hide the scoreboard, and temporary tokens can expire. `goalclip` can run on your computer or a server you manage. On a server, use `tmux`, `screen`, or a service manager to keep it running after the terminal closes. Monitor the log for goal, retraction, and review events; `--json` emits one JSON object per line.

### OBS with MediaMTX

For a stream you produce, send OBS to a local [MediaMTX](https://github.com/bluenviron/mediamtx) server and point `clipper watch` at it:

```text
Match source → OBS → MediaMTX → clipper watch → clips/
                         └→ optional platform forwarding
```

The streamer must have the right to show and clip the match. Do not use this setup to pull video from a protected app; a black capture may mean the app protects its video.

1. **Run MediaMTX.** Download the single-file release for your OS from the [MediaMTX repository](https://github.com/bluenviron/mediamtx) and run it. By default, it accepts RTMP on port `1935`.
2. **Configure OBS.** Under **Settings → Stream**, choose **Custom**; set **Server** to `rtmp://localhost:1935/live` and **Stream key** to `match`. Start streaming.
3. **Start goalclip** in another terminal:
   ```bash
   ./clipper watch rtmp://localhost:1935/live/match
   ```
4. **Optionally forward the feed** to another platform. Add this to `mediamtx.yml` and replace the platform placeholders:
   ```yaml
   paths:
     live/match:
       runOnReady: ffmpeg -i rtsp://localhost:$RTSP_PORT/$MTX_PATH -c copy -f flv rtmp://PLATFORM_INGEST_URL/STREAM_KEY
       runOnReadyRestart: yes
   ```
   OBS sends to MediaMTX, which forwards a copy. Keep the stream key private; `mediamtx.yml` is in `.gitignore`. A third-party OBS multi-output plugin is another option, but check version compatibility.
5. **Use compatible output settings:** H.264, constant bitrate (CBR), a two-second keyframe interval, and AAC audio. Short keyframe intervals also help keep clip boundaries close to the goal.
6. **Keep the scoreboard visible** in the OBS output. Do not cover it with a webcam, chat, or overlay. If it is hidden, score detection cannot work; use `--audio-only` as a fallback. Keep match audio in the stream or set `audio.enabled` to `false`.
7. **Check before kick-off:**
   ```bash
   ./clipper calibrate rtmp://localhost:1935/live/match
   ```
   Confirm the red box covers the score, then start `watch` before kick-off.

To run `goalclip` on another computer, use that computer's address instead of `localhost`. Expose MediaMTX only on a trusted network or enable its password protection.

## Testing

### Unit tests

```bash
go vet ./...
go test ./...
```

### Local replay test

Use a recording you are allowed to use: your own, SoccerNet research data, or a replay whose owner permits downloads. Do not bypass login or copy protection.

1. Cut a section with a couple of goals, starting a few minutes before the first:
   ```bash
   ffmpeg -ss 00:10:00 -i full-match.mp4 -t 00:20:00 -c copy replay.mp4
   ```
2. Check scoreboard detection and inspect `clips/scoreboard-detected.png`:
   ```bash
   ./clipper calibrate replay.mp4
   ```
3. Run detection:
   ```bash
   ./clipper replay replay.mp4 --speed 8
   ```

Example log (times in square brackets are match times in the input):

```text
[00:00] clips for this run go to clips/2026-10-28_153012_replay
[00:00] scoreboard found at x=145 y=20 w=133 h=49, reads 0-0 (seen in 83% of samples); preview saved to ...
[03:46] score 0-0 -> 1-0
[03:46] GOAL candidate: score 0-0 -> 1-0, confirming for 15s
[03:40] GOAL 1-0 confirmed (confidence 1.00): score change confirmed by audio spike
[03:40] saved clips/2026-10-28_153012_replay/replay_003m40s_1-0.mp4
```

The score-change and candidate messages can appear after the goal. The confirmed-goal line uses the estimated goal time, not the later confirmation time.

### URL replay test

`replay` also accepts a direct video or stream URL. Run `check` first; if FFmpeg reports an error, see [Troubleshooting](#troubleshooting). For example, the Internet Archive Barcelona–PSG recording above can be checked and replayed with:

```bash
./clipper check "https://archive.org/download/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2/2017.03.08%20-%20UCL-R16-G2%20Barcelona%20PSG%206-1.mp4"
./clipper replay "https://archive.org/download/2017.03.08-ucl-r-16-g-2-barcelona-psg-6-2/2017.03.08%20-%20UCL-R16-G2%20Barcelona%20PSG%206-1.mp4" --speed 0
```

Quote URLs and encode spaces as `%20`. Full matches are long; press `Ctrl-C` when you have seen enough. To fetch only part of a recording (if the server supports partial downloads):

```bash
ffmpeg -ss 00:20:00 -i "<direct-url>" -t 00:10:00 -c copy sample.mp4
./clipper replay sample.mp4 --speed 8
```

Inspect the server response with `curl -sIL "<direct-url>"`; `Content-Type` should be `video/...`, not `text/html`. Scoreboard search starts at the beginning of the file, so start later if the opening contains titles or highlights without a scoreboard.

### Simulated live-stream test

Start MediaMTX as described in [OBS with MediaMTX](#obs-with-mediamtx), then run the following in separate terminals:

```bash
# Terminal 1: publish a recording as a live stream
ffmpeg -re -i replay.mp4 -c copy -f flv rtmp://localhost:1935/live/match

# Terminal 2: monitor it
./clipper watch rtmp://localhost:1935/live/match
```

Start `watch` before the relevant section, ideally before kick-off, to allow scoreboard discovery.

### Accuracy evaluation

`clipper eval` processes every match in a directory and compares detections with known goal times. Each match needs a video and a matching `.goals.json` file; an optional `.config.json` can override its settings:

```text
testdata/matches/
├── match1.mp4
├── match1.goals.json   # {"goals":[{"time":202.0},{"time":2710.5}]}
└── match1.config.json  # optional
```

Goal times are seconds from the start of the file. If a match config is absent, `--config` or built-in defaults are used.

```bash
./clipper eval testdata/matches --speed 0
```

The report includes precision (how many detections were real goals), recall (how many real goals were found), average timing error, and clip coverage (how often the clip contains the goal). Default targets are **90% recall**, **85% precision**, and **95% clip coverage**. The command exits with an error if a target is missed. A detection is correct when it is within 25 seconds of a labelled goal; change this with `--tolerance`.

Convert SoccerNet labels with:

```bash
./clipper labels Labels-v2.json --half 1 > 1_HQ.goals.json
```

Treat each half as its own file. Include difficult cases in the dataset: replays of earlier goals, VAR checks, disallowed goals, and advertising breaks.

## Troubleshooting

Start with `./clipper check <input>` and add `--debug` to any command for detailed logs.

### Input errors

| Error or symptom | Likely cause / fix |
|---|---|
| `Invalid data found when processing input` | The URL is a web page, not a video, or the download is damaged. Use a direct video URL or download again. |
| `EBML header parsing failed` | An MKV/WebM URL returned a page or error, or the file is damaged. Try the URL in a browser, use an MP4, or download again. |
| `moov atom not found` | The MP4 is incomplete. Download it again. |
| `Server returned 403` or `401` | Login is required, a token expired, or the server blocks the request. Use an accessible public URL. |
| `Server returned 404` | The address is wrong or the resource is gone. Check spelling and encode spaces as `%20`. |
| `Failed to resolve hostname` | The hostname is mistyped or the network is unavailable. |
| `Connection refused` | Nothing is listening there. With OBS, start MediaMTX and streaming, then check the address and stream key. |
| `Protocol not found` | FFmpeg lacks the protocol (SRT is a common example). Check `ffmpeg -protocols` or install a fuller build. |
| `Stream map '0:a:0' matches no streams` | No audio track exists. For `watch`, set `"audio": {"enabled": false}`; `replay` detects missing audio itself. |
| `No such file or directory` | The file path is wrong. Quote paths containing spaces. |
| `ffprobe not found` | Input checking is skipped. Install FFprobe (usually bundled with FFmpeg) or pass `--ffprobe` to `check`. |

### Runtime issues

| Message or symptom | Likely cause / fix |
|---|---|
| `input ended, restarting` repeatedly | `watch` is being used on a recording. Use `replay`. |
| `no data from ffmpeg, restarting it` | The stream stalled. Check the source/network or increase `ingest.stall_timeout_seconds` if startup is slow. |
| `tesseract not found` | Install Tesseract, pass `--tesseract <path>`, or use `--audio-only`. |
| No pictures arrive; the process waits | OBS is not streaming, or the stream key does not match the URL. |
| `searching the first 30 minutes for the scoreboard` | Normal replay behaviour; long pre-match coverage makes discovery take longer. |
| `no scoreboard in the opening part of the video; the search continues while it plays` | The recording starts before the scoreboard appears. Let the search continue or start later. |
| `still looking for the scoreboard` | The scoreboard is not yet visible, is too small, or uses an unsupported format. Run `calibrate`, set its position manually, or use `--audio-only`. |
| Wrong text is read as the score | Inspect the red box from `calibrate` and configure the scoreboard position manually. |
| Replay is slow | See [Speed and performance](#speed-and-performance). |
| Clips start late or end early | Review `score_lag_seconds`, `pre_seconds`, and `post_seconds`. |
| Too many `_review` clips | Increase `fusion.review_min_confidence` or set `fusion.review_clips` to `false`. |
| A clip ends in `_retracted` | The score dropped back after the goal; see [VAR, offside, and reversed goals](#var-offside-and-reversed-goals). |
| Platform rejects the relayed stream | Match OBS output settings to the platform's requirements. |

## Limitations

- Automatic discovery expects the home and away scores as two numbers separated by a dash or colon. Separate boxes without a separator are not found automatically.
- Goal detection does not start until the scoreboard is found, unless `--audio-only` or `audio_fallback` is enabled. Goals before discovery can be missed.
- A score of 10 or higher may be wider than the detected box; configure a wider box manually if needed.
- The analysed picture, audio, and video segments can be out of sync by about one second.
- `score_lag_seconds` (default: 8) is an estimate used only when no crowd roar is found near a score change.
- A long static image, such as a half-time graphic, can pause detection as if the stream had frozen.
- VAR/offside handling depends on broadcast evidence. Late reversals and disallowed goals without a score change or clear roar may be missed.
- The tool is designed for football goals, not general sports highlights.

## Contributing

Contributions are welcome: bug reports, fixes, tests against other broadcasters, documentation improvements, and feature ideas.

**Report a problem:** Open an issue with the command you ran, logs (use `--debug`), your OS, and the output of `ffmpeg -version` and `tesseract --version`. If scoreboard detection failed, attach `scoreboard-detected.png`. Never post stream keys, tokenised URLs, or private addresses.

**Suggest a feature:** For substantial changes, open an issue first so the approach can be discussed.

**Submit a change:**

1. Fork the repository and create a branch.
2. Make the change and add tests; existing `*_test.go` files show the project style.
3. Run `gofmt -l .` (it should print nothing), `go vet ./...`, and `go test ./...`.
4. Open a pull request describing what changed, why, and how it was tested. Update the README when behaviour or settings change.

By contributing, you agree that your contribution is licensed under the project's MIT License.

## License

This project is licensed under the [MIT License](LICENSE).

FFmpeg and Tesseract are external runtime dependencies. They are not bundled with or linked into `goalclip` and are distributed under their respective licences.