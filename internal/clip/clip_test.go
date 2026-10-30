package clip

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarkRetractedRenamesClipAndRecordsWhy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "match_003m40s_1-0.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteMeta(path, Meta{File: filepath.Base(path), Home: 1, Away: 0}); err != nil {
		t.Fatal(err)
	}

	newPath, err := MarkRetracted(path, "score fell back to 0-0")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(newPath) != "match_003m40s_1-0_retracted.mp4" {
		t.Errorf("new path %q", newPath)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the original clip should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "match_003m40s_1-0.json")); !os.IsNotExist(err) {
		t.Error("the original JSON should be gone")
	}

	b, err := os.ReadFile(filepath.Join(dir, "match_003m40s_1-0_retracted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Retracted || m.RetractReason != "score fell back to 0-0" || m.File != filepath.Base(newPath) {
		t.Errorf("metadata %+v", m)
	}
}

func TestMarkRetractedWithoutMetadataStillRenames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath, err := MarkRetracted(path, "r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("renamed clip missing: %v", err)
	}
}

func TestRunFolderAndBaseName(t *testing.T) {
	ts := time.Date(2026, 10, 28, 15, 30, 12, 0, time.UTC)
	if got := RunFolder("Barca v PSG!", ts); got != "2026-10-28_153012_Barca_v_PSG" {
		t.Errorf("run folder %q", got)
	}
	if got := RunFolder("", ts); got != "2026-10-28_153012_match" {
		t.Errorf("empty name gave %q", got)
	}
	if got := BaseName("replay", 220, 1, 0); got != "replay_003m40s_1-0" {
		t.Errorf("base name %q", got)
	}
	if got := BaseName("replay", 220, -1, -1); got != "replay_003m40s_audio" {
		t.Errorf("audio-only base name %q", got)
	}
}
