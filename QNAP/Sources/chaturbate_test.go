package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// findWorkingFFmpeg returns a system ffmpeg that actually runs, or "" when
// none works (e.g. a half-upgraded brew install or no ffmpeg at all).
func findWorkingFFmpeg(t *testing.T) string {
	t.Helper()
	cands := []string{os.Getenv("FFMPEG")}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands,
		"/opt/homebrew/opt/ffmpeg@7/bin/ffmpeg",
		"/opt/homebrew/opt/ffmpeg/bin/ffmpeg",
	)
	seen := map[string]bool{}
	for _, c := range cands {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if st, err := os.Stat(c); err != nil || st.IsDir() {
			continue
		}
		cmd := exec.Command(c, "-version")
		cmd.Env = append(os.Environ(),
			"DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/opt/x265/lib:/opt/homebrew/opt/x264/lib")
		if out, err := cmd.CombinedOutput(); err == nil && bytes.Contains(out, []byte("ffmpeg version")) {
			return c
		}
	}
	return ""
}

// sampleRoomHTML mimics a chaturbate room page: it embeds the HLS master URL
// inside a JS string terminated by \u0022 and HTML-escapes it like the real
// site does.
const sampleRoomHTML = `<html><head>
<meta property="og:title" content="Watch XQC_cb live on Chaturbate!">
<meta property="og:description" content="Jak tam ladies? &amp; gentlemen 😉">
<meta property="og:image" content="https://cb.img/avatar.jpg">
</head><body>
<div id="room-topic">room topic text</div>
<script>var stream = "https:\/\/edge.cb.com\/hls\/xqc_cb\/index.m3u8?token=abc123&amp;expires=9999\u0022</script>
</body></html>`

func TestChaturbateExtractM3U8(t *testing.T) {
	got := cbM3U8(sampleRoomHTML)
	want := "https://edge.cb.com/hls/xqc_cb/index.m3u8?token=abc123&expires=9999"
	if got != want {
		t.Errorf("cbM3U8 = %q, want %q", got, want)
	}
	if got := cbM3U8("<html>offline room, no m3u8</html>"); got != "" {
		t.Errorf("offline room should have no m3u8, got %q", got)
	}
}

func TestChaturbateParseMeta(t *testing.T) {
	if got := cbTitle(sampleRoomHTML); got != "Jak tam ladies? & gentlemen 😉" {
		t.Errorf("cbTitle = %q", got)
	}
	if got := cbAvatar(sampleRoomHTML); got != "https://cb.img/avatar.jpg" {
		t.Errorf("cbAvatar = %q", got)
	}
}

// TestChaturbateLiveAndMaster verifies the full flow against a fake room page:
// live detection, title/avatar parsing and the HLS master being fetched with a
// Referer, with offline and 404 rooms handled correctly.
func TestChaturbateLiveAndMaster(t *testing.T) {
	offline := `<html><head><meta property="og:title" content="Watch Off room live on Chaturbate!"></head><body></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			if r.Header.Get("Referer") == "" {
				http.Error(w, "missing referer", http.StatusForbidden)
				return
			}
			w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080\nchunklist.m3u8\n"))
			return
		}
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "/live":
			w.Write([]byte(sampleRoomHTML))
		case "/off":
			w.Write([]byte(offline))
		case "/nope":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// our functions hit chaturbate.com directly; rewrite the host through a
	// redirect transport so the real hostname path is exercised
	cb := &http.Client{Transport: redirectTransport{srv.URL}}

	live, title, err := chaturbateLive(cb, "live")
	if err != nil || !live {
		t.Fatalf("chaturbateLive(live) = live=%v title=%q err=%v", live, title, err)
	}
	if !strings.Contains(title, "ladies") {
		t.Errorf("title = %q", title)
	}

	live, _, err = chaturbateLive(cb, "off")
	if err != nil || live {
		t.Errorf("offline room: live=%v err=%v, want offline", live, err)
	}

	if _, _, err := chaturbateLive(cb, "nope"); err == nil || !strings.Contains(err.Error(), "nie istnieje") {
		t.Errorf("404 room should report non-existing, got %v", err)
	}

	master, base, err := chaturbateMaster(cb, "live")
	if err != nil {
		t.Fatalf("chaturbateMaster: %v", err)
	}
	if !strings.Contains(master, "chunklist.m3u8") {
		t.Errorf("master = %q", master)
	}
	if !strings.HasPrefix(base, "https://edge.cb.com/") {
		t.Errorf("base = %q", base)
	}

	if got := chaturbateAvatar(cb, "live"); got != "https://cb.img/avatar.jpg" {
		t.Errorf("avatar = %q", got)
	}
}

// redirectTransport rewrites the host of chaturbate URL requests to the fake
// test server so the real hostname path is exercised.
type redirectTransport struct{ base string }

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := t.base[strings.Index(t.base, "://")+3:]
	u := *r.URL
	u.Scheme = "http"
	u.Host = host
	r2 := r.Clone(r.Context())
	r2.URL = &u
	return http.DefaultTransport.RoundTrip(r2)
}

// TestChaturbateBestStreams verifies the master playback: the best video
// variant and its AUDIO-group audio playlist are resolved to absolute URLs,
// audio-only variants are skipped, and a single rendition with muxed audio
// keeps an empty audio URL.
func TestChaturbateBestStreams(t *testing.T) {
	master := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio_aac_96",NAME="Audio 96",DEFAULT=YES,URI="audio/audio96.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio_aac_128",NAME="Audio 128",DEFAULT=NO,URI="audio/audio128.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=8388608,RESOLUTION=1920x1080,AUDIO="audio_aac_96"
video/1080.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4194304,RESOLUTION=1280x720,AUDIO="audio_aac_128"
video/720.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1179648,RESOLUTION=854x480,AUDIO="audio_aac_128"
video/480.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=999999999,RESOLUTION=0x0
audio_only.m3u8
`
	best, err := cbBestStreams(master, "https://edge.cb.com/hls/xqc_cb/index.m3u8?token=abc")
	if err != nil {
		t.Fatal(err)
	}
	if best.video != "https://edge.cb.com/hls/xqc_cb/video/1080.m3u8" {
		t.Errorf("video = %q", best.video)
	}
	if best.audio != "https://edge.cb.com/hls/xqc_cb/audio/audio96.m3u8" {
		t.Errorf("audio = %q", best.audio)
	}
	if best.res != "1920x1080" {
		t.Errorf("res = %q", best.res)
	}

	// a single rendition with muxed audio (no EXT-X-MEDIA): audio stays empty
	single := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\nmedia.m3u8\n"
	best, err = cbBestStreams(single, "https://edge.cb.com/a/b/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if best.video != "https://edge.cb.com/a/b/media.m3u8" || best.audio != "" || best.res != "1280x720" {
		t.Errorf("single rendition = %+v", best)
	}

	// a master advertising no video rendition is an error
	if _, err := cbBestStreams("#EXTM3U\n#EXT-X-ENDLIST\n", ""); err == nil {
		t.Error("expected an error for a master without video renditions")
	}
}

// TestChaturbateRecordTS drives Recorder.runChaturbate against a fake live room
// and a real TS HLS (served locally) and verifies the whole pipeline: the
// variant+audio selection from the master, ffmpeg capturing both inputs into a
// .ts, and the auto conversion into a .mp4 that still contains video AND audio.
// That is the regression test for older chaturbate recordings that silently
// lost all audio (only the video variant was captured) and for files that never
// appeared at all.
func TestChaturbateRecordTS(t *testing.T) {
	ff := findWorkingFFmpeg(t)
	if ff == "" {
		t.Skip("no runnable system ffmpeg available")
	}
	old := cbPollInterval
	oldGrace := cbStopGrace
	cbPollInterval = 200 * time.Millisecond
	cbStopGrace = 3 * time.Second
	defer func() { cbPollInterval, cbStopGrace = old, oldGrace }()

	// 1. Build a short TS HLS fixture (video + muxed aac audio).
	fix := filepath.Join(t.TempDir(), "hls")
	if err := os.MkdirAll(fix, 0o755); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command(ff, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=8:size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=8",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-sc_threshold", "0",
		"-c:a", "aac",
		"-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod",
		filepath.Join(fix, "out.m3u8"))
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("TS fixture generation failed: %v\n%s", err, out)
	}
	vod, err := os.ReadFile(filepath.Join(fix, "out.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vod), ".ts") {
		t.Fatalf("unexpected fixture playlist: %s", vod)
	}
	// the served playlist is a *live* one: same segments, no ENDLIST
	live := strings.Replace(string(vod), "#EXT-X-ENDLIST", "", 1)
	segs := map[string][]byte{}
	entries, err := os.ReadDir(fix)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			if b, err := os.ReadFile(filepath.Join(fix, e.Name())); err == nil {
				segs["/"+e.Name()] = b
			}
		}
	}

	// 2. Fake room + master mirroring Chaturbate's real layout: video variants
	// each referencing a separate audio rendition group.
	login := "cb_fixture"
	var liveNow int32 = 1
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n" +
				`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio_aac_128",NAME="Audio 128",DEFAULT=NO,URI="audio/audio.m3u8"` + "\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,AUDIO=\"audio_aac_128\"\n" +
				"video/video.m3u8\n"))
		case "/video/video.m3u8", "/audio/audio.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte(live))
		case "/" + login + "/":
			if atomic.LoadInt32(&liveNow) == 0 {
				// offline page: no m3u8 embedded
				w.Write([]byte(`<html><head><meta property="og:title" content="Watch Off room live on Chaturbate!"></head><body></body></html>`))
				return
			}
			masterEsc := strings.ReplaceAll(srv.URL+"/master.m3u8", "/", `\/`)
			w.Write([]byte(strings.Replace(sampleRoomHTML,
				"https:\\/\\/edge.cb.com\\/hls\\/xqc_cb\\/index.m3u8?token=abc123&amp;expires=9999",
				masterEsc, 1)))
		default:
			// playlists are served from /video/ and /audio/, so relative
			// segment URIs resolve below them; find the segment by its base name
			if b, ok := segs["/"+filepath.Base(r.URL.Path)]; ok {
				w.Header().Set("Content-Type", "video/mp2t")
				w.Write(b)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 3. Run the recorder against the fake room (the app fetches the room page
	// and master via host rewriting; ffmpeg fetches the playlists and segments
	// straight from the local server).
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegOverride = ff
	a.mu.Lock()
	a.cfg.OutDir = filepath.Join(dir, "rec")
	a.cfg.AutoMP4 = true
	a.mu.Unlock()
	a.httpc = &http.Client{Transport: redirectTransport{srv.URL}}

	r := &Recorder{app: a, login: login, platform: "chaturbate",
		stop: make(chan struct{}), log: []string{}, convertOnStop: atomic.Bool{}}
	done := make(chan struct{})
	go func() { r.run(); close(done) }()

	// Wait for real data, then let the room go offline so the auto conversion
	// into .mp4 runs (the offline path uses cfg.AutoMP4).
	var gotSize int64
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if r.out != "" {
			if st, err := os.Stat(r.out); err == nil {
				gotSize = st.Size()
				if gotSize > 100<<10 {
					atomic.StoreInt32(&liveNow, 0)
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if gotSize <= 100<<10 {
		atomic.StoreInt32(&liveNow, 0)
		r.logMu.Lock()
		t.Logf("recorder log:\n%s", strings.Join(r.log, "\n"))
		r.logMu.Unlock()
		if r.out != "" {
			if st, err := os.Stat(r.out); err == nil {
				t.Logf("out file %s size=%d", r.out, st.Size())
			}
		}
		t.Fatal("recording produced no data")
	}

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("recorder did not stop")
	}

	// 4. The finished file must be a .mp4 with h264 video AND aac audio — the
	// whole point of replacing the video-only fragment collector.
	mp4s, err := filepath.Glob(filepath.Join(dir, "rec", "chaturbate", login, "*.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mp4s) == 0 {
		var tree strings.Builder
		_ = filepath.Walk(filepath.Join(dir, "rec"), func(p string, info os.FileInfo, err error) error {
			if err == nil {
				fmt.Fprintf(&tree, "%s %d\n", p, info.Size())
			}
			return nil
		})
		t.Logf("rec tree:\n%s", tree.String())
		t.Fatalf("no .mp4 recorded (looking in %s)", filepath.Join(dir, "rec", "chaturbate", login))
	}
	probe := exec.Command(ff, "-i", mp4s[0])
	out, _ := probe.CombinedOutput()
	if !strings.Contains(string(out), "Audio: aac") || !strings.Contains(string(out), "Video: h264") {
		t.Fatalf("recording must contain h264 video and aac audio:\n%s", out)
	}
}
