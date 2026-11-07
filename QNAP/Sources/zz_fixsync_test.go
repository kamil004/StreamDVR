package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// findFF returns a usable ffmpeg for the tests, or skips when none exists.
func findFF(t *testing.T) string {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found, skipping av sync fix test")
	}
	return ff
}

func TestFixAVSync(t *testing.T) {
	ff := findFF(t)

	// Build a synthetic "recording" where the audio runs 2 seconds EARLIER than
	// the video (audio_first − video_first = −2), which mirrors the real-world
	// Chaturbate rooms the user tested.
	dir := t.TempDir()
	v0 := filepath.Join(dir, "v0.ts")
	a0 := filepath.Join(dir, "a0.ts")
	final := filepath.Join(dir, "rec.ts")
	mustFF(t, ff,
		"-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=320x240:rate=30",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mpegts", "-t", "6", v0)
	mustFF(t, ff,
		"-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6:r=48000",
		"-af", "aresample=48000", "-c:a", "aac", "-b:a", "128k", "-f", "mpegts", "-t", "6", a0)
	// Shift the VIDEO input +2 s so the audio appears 2 s early in the merge.
	mustFF(t, ff,
		"-nostdin", "-y", "-v", "error",
		"-copyts", "-itsoffset", "2", "-i", v0,
		"-copyts", "-i", a0,
		"-map", "0:v:0", "-map", "1:a:0",
		"-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", final)

	d, err := tsAVDelta(final)
	if err != nil {
		t.Fatalf("tsAVDelta: %v", err)
	}
	if d > -1.0 {
		t.Fatalf("expected audio to lead by ~2s, got delta=%.3f", d)
	}

	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegOverride = ff

	if n := a.fixAVSync(final); n <= 0 {
		t.Fatalf("fixAVSync returned %d, expected a corrected file", n)
	}
	d, err = tsAVDelta(final)
	if err != nil {
		t.Fatalf("tsAVDelta after fix: %v", err)
	}
	if d < -avSyncThreshold || d > avSyncThreshold {
		t.Fatalf("after fix delta=%.3f, expected |delta|<=%.3f", d, avSyncThreshold)
	}

	// A recording that is already in sync must be left untouched.
	synced := filepath.Join(dir, "ok.ts")
	mustFF(t, ff,
		"-nostdin", "-y", "-v", "error",
		"-copyts", "-i", v0,
		"-copyts", "-i", a0,
		"-map", "0:v:0", "-map", "1:a:0",
		"-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", synced)
	if n := a.fixAVSync(synced); n != 0 {
		t.Fatalf("in-sync recording was modified (returned %d)", n)
	}
}

func mustFF(t *testing.T, ff string, args ...string) {
	t.Helper()
	cmd := exec.Command(ff, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v (%s)", args, err, out)
	}
}