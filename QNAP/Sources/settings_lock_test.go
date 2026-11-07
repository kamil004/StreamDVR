package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSettingsLockFlow(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Token = "tok-123"
	a.cfg.Username = "bob"
	a.save()

	if a.settingsLocked() {
		t.Fatal("settings should start unlocked without a password")
	}

	if err := a.setPassword("", "secret1"); err != nil {
		t.Fatalf("setPassword: %v", err)
	}
	// settings stay usable in this session, but the token files on disk are
	// already encrypted; after lockSettings / restart they are locked again
	if a.settingsLocked() {
		t.Fatal("settings should stay available after pwset until lockSettings")
	}

	raw, err := os.ReadFile(a.cfgFile)
	if err != nil {
		t.Fatalf("config.json must still exist: %v", err)
	}
	if bytes.Contains(raw, []byte("tok-123")) || bytes.Contains(raw, []byte("bob")) {
		t.Fatal("config.json must NOT contain the token/username when admin password is set")
	}
	if _, err := os.Stat(a.encFile); err != nil {
		t.Fatalf("settings.enc must exist: %v", err)
	}

	a.lockSettings()
	if !a.settingsLocked() {
		t.Fatal("settings must be locked after lockSettings")
	}
	if err := a.unlockSettings("wrongpass"); err == nil {
		t.Fatal("wrong password must fail")
	}
	if err := a.unlockSettings("secret1"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if a.cfg.Token != "tok-123" {
		t.Fatal("token not restored after unlock")
	}

	// changing the password requires the current one
	if err := a.setPassword("nope", "newpass"); err == nil {
		t.Fatal("changing password with wrong current must fail")
	}
	if err := a.setPassword("secret1", "newpass"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	a.lockSettings()
	if !a.settingsLocked() {
		t.Fatal("settings must be locked after lockSettings")
	}
	if err := a.unlockSettings("secret1"); err == nil {
		t.Fatal("old password must not work after change")
	}
	if err := a.unlockSettings("newpass"); err != nil {
		t.Fatalf("unlock with new password: %v", err)
	}
}

// TestConvertMP4 uses a short ffmpeg-generated ts fixture to exercise the real
// conversion pipeline. Skips when ffmpeg is unavailable.
func TestConvertMP4(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("system ffmpeg not available")
	}
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "test.ts")
	cmd := exec.Command(ff, "-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=30",
		"-f", "mpegts", "-y", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("fixture generation failed: %v\n%s", err, out)
	}
	st, _ := os.Stat(src)
	if st == nil || st.Size() == 0 {
		t.Fatal("fixture not generated")
	}

	out, err := a.convertMP4(src)
	if err != nil {
		t.Fatalf("convertMP4: %v", err)
	}
	if !strings.HasSuffix(out, ".mp4") {
		t.Fatalf("expected .mp4 output, got %q", out)
	}
	mo, err := os.Stat(out)
	if err != nil || mo.Size() == 0 {
		t.Fatalf("mp4 missing/empty: %v", err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Fatal("source .ts should have been removed after successful conversion")
	}
}

// TestSettingsLockHTTP locks the Settings and verifies the recording panel
// (add channel / status) keeps working while password-protected actions do not.
func TestSettingsLockHTTP(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setPassword("", "admin123"); err != nil {
		t.Fatal(err)
	}
	a.lockSettings()

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	post := func(path, body string) map[string]interface{} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		return m
	}

	// recording panel stays free while locked
	if m := post("/api/channels", `{"name":"mychan"}`); m["error"] != nil {
		t.Fatalf("adding a channel must work while settings locked: %v", m)
	}
	// settings-guarded actions are rejected
	if m := post("/api/pwset", `{"current":"admin123","password":"x12345"}`); m["error"] == nil {
		t.Fatal("pwset must be rejected while settings locked")
	}
	if m := post("/api/login", `{"token":"unused"}`); m["error"] == nil {
		t.Fatal("login must be rejected while settings locked")
	}

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var st struct {
		SettingsLocked bool `json:"settings_locked"`
		Channels       []struct {
			Login string `json:"login"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(b, &st)
	if !st.SettingsLocked {
		t.Fatal("status must report settings_locked")
	}
	if len(st.Channels) != 1 || st.Channels[0].Login != "mychan" {
		t.Fatalf("channels must be visible while locked, got %+v", st.Channels)
	}

	// unlock, then pwset works with the current password
	if m := post("/api/unlock", `{"password":"admin123"}`); m["error"] != nil {
		t.Fatalf("unlock: %v", m)
	}
	if m := post("/api/pwset", `{"current":"admin123","password":"newpass1"}`); m["error"] != nil {
		t.Fatalf("pwset after unlock: %v", m)
	}
}

var _ = bytes.NewBuffer

// TestExportImport verifies the settings export download and the guarded
// import round-trip (channels, tokens, out_dir).
func TestExportImport(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Token = "twtok"
	a.cfg.KickToken = "kicktok"
	a.cfg.OutDir = "/share/Public/StreamDVR"
	a.cfg.Channels = []Channel{
		{Name: "xqc", Login: "xqc", Platform: "twitch"},
		{Name: "kick_esports", Login: "kick_esports", Platform: "kick"},
	}
	a.save()

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	exp, err := http.Get(ts.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(exp.Body)
	exp.Body.Close()
	if exp.StatusCode != 200 {
		t.Fatalf("export status %d: %s", exp.StatusCode, b)
	}
	if !bytes.Contains(b, []byte("twtok")) || !bytes.Contains(b, []byte("kicktok")) {
		t.Fatalf("export must include the tokens: %s", b)
	}
	if !bytes.Contains(b, []byte("kick_esports")) {
		t.Fatalf("export must include channels: %s", b)
	}

	// import into a fresh app via the HTTP route
	b2, _ := os.ReadFile(a.cfgFile)
	_ = os.WriteFile(filepath.Join(dir, "data", "import.json"), b2, 0o600)

	a2, err := NewApp(filepath.Join(dir, "root2"), filepath.Join(dir, "data2"))
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(a2.setupRoutes())
	defer ts2.Close()

	resp, err := http.Post(ts2.URL+"/api/import", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("import status %d", resp.StatusCode)
	}
	a2.mu.Lock()
	cfg2 := a2.cfg
	a2.mu.Unlock()
	if cfg2.Token != "twtok" || cfg2.KickToken != "kicktok" {
		t.Fatalf("import did not restore tokens: token=%q kick=%q", cfg2.Token, cfg2.KickToken)
	}
	if len(cfg2.Channels) != 2 || a2.findChannel("xqc", "twitch") < 0 || a2.findChannel("kick_esports", "kick") < 0 {
		t.Fatalf("import did not restore channels: %+v", cfg2.Channels)
	}
}

// TestDiskFree reports a non-negative free-space value for a temporary dir.
func TestDiskFree(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = filepath.Join(dir, "recordings")
	_ = os.MkdirAll(a.cfg.OutDir, 0o755)
	if n := a.diskFree(a.cfg.OutDir); n <= 0 {
		t.Fatalf("diskFree(%s) = %d, want > 0", a.cfg.OutDir, n)
	}
}

// TestPerPlatformDuplicate verifies the same nickname can be added on both
// Twitch and Kick, and that a duplicate is only rejected for the same platform.
func TestPerPlatformDuplicate(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	post := func(body string) map[string]interface{} {
		resp, err := http.Post(ts.URL+"/api/channels", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m
	}

	if m := post(`{"name":"xqc","platform":"twitch"}`); m["error"] != nil {
		t.Fatalf("adding twitch xqc: %v", m["error"])
	}
	if m := post(`{"name":"xqc","platform":"twitch"}`); m["error"] == nil {
		t.Fatal("duplicate twitch xqc must be rejected")
	}
	if m := post(`{"name":"xqc","platform":"kick"}`); m["error"] != nil {
		t.Fatalf("adding kick xqc must be allowed: %v", m["error"])
	}

	a.mu.Lock()
	chs := a.cfg.Channels
	a.mu.Unlock()
	if len(chs) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(chs))
	}
	// the duplicate check must only apply per (login, platform) pair
	twitch := a.findChannel("xqc", "twitch")
	kick := a.findChannel("xqc", "kick")
	if twitch < 0 || kick < 0 || twitch == kick {
		t.Fatalf("expected separate indices for both platforms, twitch=%d kick=%d", twitch, kick)
	}
}

func TestSettingsRateLimit(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setPassword("", "admin123"); err != nil {
		t.Fatal(err)
	}
	a.lockSettings()

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	tap := func(client string) map[string]interface{} {
		b, _ := json.Marshal(map[string]string{"client": client})
		r, err := http.Post(ts.URL+"/api/settings/tap", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var m map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		return m
	}

	// 5 failed taps block this client (block kicks in on the 5th tap)
	for i := 0; i < 4; i++ {
		if m := tap("browser-a"); m["ok"] != true {
			t.Fatalf("tap %d should be ok, got %v", i+1, m)
		}
	}
	if m := tap("browser-a"); m["ok"] != false {
		t.Fatalf("5th tap must be blocked, got %v", m)
	}
	// other browsers are unaffected
	if m := tap("browser-b"); m["ok"] != true {
		t.Fatalf("unrelated browser must not be blocked, got %v", m)
	}

	// rate.json persisted on disk
	if _, err := os.Stat(filepath.Join(dir, "data", "rate.json")); err != nil {
		t.Fatalf("rate.json must persist, err=%v", err)
	}

	// a blocked client cannot even unlock
	r, err := http.Post(ts.URL+"/api/unlock", "application/json", bytes.NewBufferString(`{"password":"admin123","client":"browser-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var mx map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&mx)
	if mx["error"] == nil || !strings.Contains(mx["error"].(string), "Zbyt wiele prób") {
		t.Fatalf("blocked client must not unlock, got %v", mx)
	}

	// a fresh client can unlock, and success clears its own counter
	for i := 0; i < 2; i++ {
		if m := tap("browser-c"); m["ok"] != true {
			t.Fatalf("pre-unlock tap %d should be ok, got %v", i+1, m)
		}
	}
	r2, err := http.Post(ts.URL+"/api/unlock", "application/json", bytes.NewBufferString(`{"password":"admin123","client":"browser-c"}`))
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if m := tap("browser-c"); m["ok"] != true {
		t.Fatalf("tap after successful unlock should be ok, got %v", m)
	}
}

// TestGrowingPlaylist verifies that a recorded .ts with a per-segment .idx is
// served as a live/EVENT byte-range playlist while it is being recorded (no
// ENDLIST, so hls.js keeps polling for new segments) and as a finished VOD-like
// playlist (with ENDLIST) once the recording stops.
func TestGrowingPlaylist(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = filepath.Join(dir, "recordings")
	_ = os.MkdirAll(filepath.Join(a.cfg.OutDir, "twitch", "xqc"), 0o755)

	name := "xqc_2026-09-11_12-00-00_stream.ts"
	tsPath := filepath.Join(a.cfg.OutDir, "twitch", "xqc", name)
	segs := []segInfo{{O: 0, S: 100, D: 2}, {O: 100, S: 150, D: 2}}
	if err := os.WriteFile(tsPath, make([]byte, segs[1].O+segs[1].S), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := indexOf(tsPath); ok {
		t.Fatalf("indexOf on file without sidecar should be false, got %v", got)
	}
	b, _ := json.Marshal(segs)
	if err := os.WriteFile(tsPath+".idx", b, 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()
	get := func() string {
		resp, err := http.Get(ts.URL + "/playlist/twitch/xqc/" + name)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	// no active recorder -> finished file, must end with ENDLIST
	pl := get()
	if !strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:EVENT") {
		t.Errorf("byte-range playlist should be EVENT, got:\n%s", pl)
	}
	if !strings.Contains(pl, "#EXT-X-BYTERANGE:100@0") || !strings.Contains(pl, "#EXT-X-BYTERANGE:150@100") {
		t.Errorf("missing byte-range fragments, got:\n%s", pl)
	}
	if !strings.HasSuffix(strings.TrimSpace(pl), "#EXT-X-ENDLIST") {
		t.Errorf("finished file must carry ENDLIST, got:\n%s", pl)
	}

	// simulate an active recording -> playlist must stay "live" (no ENDLIST)
	a.recsMu.Lock()
	a.recs["twitch:xqc"] = &Recorder{app: a, login: "xqc", platform: "twitch", out: tsPath}
	a.recsMu.Unlock()
	if strings.Contains(get(), "#EXT-X-ENDLIST") {
		t.Errorf("growing file must NOT carry ENDLIST, got:\n%s", get())
	}

	// growing file without a sidecar falls back to the legacy VOD playlist
	bogus := "xqc_2026-09-11_12-10-00_bogus.ts"
	_ = os.WriteFile(filepath.Join(a.cfg.OutDir, "twitch", "xqc", bogus), make([]byte, 64), 0o644)
	resp, err := http.Get(ts.URL + "/playlist/twitch/xqc/" + bogus)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Errorf("file without index should use VOD fallback, got:\n%s", string(body))
	}
}

// TestFileListHidesIdxAndClearFolder verifies that .idx sidecars are hidden
// from the recordings list, that the clear-folder endpoint wipes everything
// (including sidecars) and refuses to run while a recording is growing.
func TestFileListHidesIdxAndClearFolder(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = filepath.Join(dir, "recordings")
	folder := filepath.Join(a.cfg.OutDir, "kick", "esport")
	_ = os.MkdirAll(folder, 0o755)
	_ = os.WriteFile(filepath.Join(folder, "esport_1.ts"), make([]byte, 10), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "esport_1.ts.idx"), []byte(`[{"O":0,"S":10,"D":2}]`), 0o644)

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	// .idx must be hidden from the panel list
	resp, err := http.Get(ts.URL + "/api/files/kick/esport")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var d struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	_ = json.Unmarshal(b, &d)
	if len(d.Files) != 1 || d.Files[0].Name != "esport_1.ts" {
		t.Fatalf("expected only the .ts in the list, got %+v", d.Files)
	}

	// clear refuses while a recording is growing
	a.recsMu.Lock()
	a.recs["kick:esport"] = &Recorder{app: a, login: "esport", platform: "kick", out: filepath.Join(folder, "esport_1.ts")}
	a.recsMu.Unlock()
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/files/kick/esport", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !bytes.Contains(b2, []byte("trwa nagrywanie")) {
		t.Fatalf("clear during active recording must be refused, got %s", b2)
	}

	// stop the recording, then clearing wipes the folder (media + sidecar)
	a.recsMu.Lock()
	delete(a.recs, "kick:esport")
	a.recsMu.Unlock()
	req3, _ := http.NewRequest("DELETE", ts.URL+"/api/files/kick/esport", nil)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if !bytes.Contains(b3, []byte(`"ok":true`)) {
		t.Fatalf("clear should succeed, got %s", b3)
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 0 {
		t.Fatalf("folder should be empty after clear, entries=%v err=%v", entries, err)
	}
}

// TestRemuxMP4 verifies that a fragmented fMP4 (the raw LL-HLS result that
// browsers often refuse to play) is remuxed into a plain fast-start MP4: no
// moof boxes left, moov placed before mdat, and the media still decodes with
// a meaningful duration.
func TestRemuxMP4(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("system ffmpeg not available")
	}
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}

	// a real fragmented mp4 (init moov + moof/mdat), like the file older
	// versions of the chaturbate recorder left behind
	src := filepath.Join(dir, "frag.mp4")
	gen := exec.Command(ff, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		"-movflags", "frag_keyframe+empty_moov+separate_moof",
		"-f", "mp4", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("fMP4 fixture generation failed: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(src)
	if !bytes.Contains(raw, []byte("moof")) {
		t.Fatal("fixture must be fragmented (contains moof)")
	}

	out, err := a.remuxMP4(src)
	if err != nil {
		t.Fatalf("remuxMP4: %v", err)
	}
	if out != src {
		t.Fatalf("expected in-place replacement, got %q", out)
	}
	remuxed, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(remuxed, []byte("moof")) {
		t.Fatal("remuxed file must be flat (no moof boxes)")
	}
	moov := bytes.Index(remuxed, []byte("moov"))
	mdat := bytes.Index(remuxed, []byte("mdat"))
	if moov < 0 || mdat < 0 || moov > mdat {
		t.Fatalf("faststart expected (moov before mdat), moov=%d mdat=%d", moov, mdat)
	}
	check := exec.Command(ff, "-v", "error", "-i", src, "-f", "null", "-")
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("remuxed file not decodable: %v\n%s", err, out)
	}
	if fprobe, err := exec.LookPath("ffprobe"); err == nil {
		probe := exec.Command(fprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", src)
		if dout, err := probe.Output(); err != nil {
			t.Fatalf("ffprobe duration failed: %v %s", err, dout)
		} else {
			var dur float64
			if _, err := fmt.Sscanf(strings.TrimSpace(string(dout)), "%f", &dur); err != nil || dur < 1.5 {
				t.Fatalf("expected >= 1.5s duration, got %q", string(dout))
			}
		}
	}
}

// TestFinalizeLeftovers verifies that a fragmented .mp4 left behind by an
// interrupted recording is remuxed on startup, while finished flat files stay
// untouched.
func TestFinalizeLeftovers(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("system ffmpeg not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "rec")
	cb := filepath.Join(out, "chaturbate", "hayleex")
	if err := os.MkdirAll(cb, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. a raw fragmented file (as if the recorder was killed mid-stream)
	frag := filepath.Join(cb, "hayleex_cut.mp4")
	gen := exec.Command(ff, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-movflags", "frag_keyframe+empty_moov", "-f", "mp4", frag)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("fMP4 fixture generation failed: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(frag)
	if !bytes.Contains(b, []byte("moof")) {
		t.Fatal("fixture must be fragmented")
	}

	// 2. a finished flat file (already finalized) that must stay untouched
	flat := filepath.Join(cb, "hayleex_ok.mp4")
	gen2 := exec.Command(ff, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=25",
		"-c:v", "libx264", "-preset", "ultrafast", flat)
	if out, err := gen2.CombinedOutput(); err != nil {
		t.Skipf("flat fixture generation failed: %v\n%s", err, out)
	}
	fi0, _ := os.Stat(flat)

	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.cfg.OutDir = out
	a.mu.Unlock()
	a.finalizeLeftovers()

	if isFragmentedMP4(frag) {
		t.Fatal("interrupted recording should have been remuxed (flattened)")
	}
	b2, _ := os.ReadFile(frag)
	if bytes.Contains(b2, []byte("moof")) {
		t.Fatal("fragmented file still contains moof after finalize")
	}
	if fi, err := os.Stat(flat); err != nil || fi.Size() != fi0.Size() {
		t.Fatalf("finished flat file must stay untouched: %v %v", fi, err)
	}

	// the remuxed file must decode
	check := exec.Command(ff, "-v", "error", "-i", frag, "-f", "null", "-")
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("finalized file not decodable: %v\n%s", err, out)
	}
}

// TestIsFragmentedMP4ProbeBound verifies the leftover detector only scans the
// beginning of a file (so startup never reads gigabytes of finished files end
// to end), while still catching real fragmented recordings whose moof sits in
// the first segment.
func TestIsFragmentedMP4ProbeBound(t *testing.T) {
	dir := t.TempDir()

	// 1 MiB of zeros: no "moof" anywhere, must not be flagged fragmented
	flat := filepath.Join(dir, "flat.mp4")
	if err := os.WriteFile(flat, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if isFragmentedMP4(flat) {
		t.Fatal("flat file must not be flagged fragmented")
	}

	// "moof" within the first 1 MiB (a real LL-HLS segment start) => true
	early := filepath.Join(dir, "early.mp4")
	b := make([]byte, 2<<20)
	copy(b[42:46], "moof")
	if err := os.WriteFile(early, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if !isFragmentedMP4(early) {
		t.Fatal("leftover with moof inside the probe window must be detected")
	}

	// "moof" only past the 1 MiB probe window => out of scope (bounded scan)
	late := filepath.Join(dir, "late.mp4")
	bl := make([]byte, 2<<20)
	copy(bl[1500<<10:], "moof")
	if err := os.WriteFile(late, bl, 0o644); err != nil {
		t.Fatal(err)
	}
	if isFragmentedMP4(late) {
		t.Fatal("moof beyond the probe window must not be scanned")
	}
}

// TestStartupNotBlockedByLeftovers verifies NewApp returns immediately (the
// panel binds right away) even when the recordings tree holds a fragmented
// leftover that gets finalized in the background.
func TestStartupNotBlockedByLeftovers(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("system ffmpeg not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "rec")
	cb := filepath.Join(out, "chaturbate", "hayleex")
	if err := os.MkdirAll(cb, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(`{"out_dir":"`+out+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	frag := filepath.Join(cb, "hayleex_cut.mp4")
	gen := exec.Command(ff, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-movflags", "frag_keyframe+empty_moov", "-f", "mp4", frag)
	if outb, err := gen.CombinedOutput(); err != nil {
		t.Skipf("fMP4 fixture generation failed: %v\n%s", err, outb)
	}

	start := time.Now()
	a, err := NewApp(filepath.Join(dir, "root"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("NewApp blocked by leftover finalize: %v", elapsed)
	}
	_ = a
	// the background remux must converge (poll briefly)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("leftover remux did not complete in the background")
		}
		if !isFragmentedMP4(frag) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestMigrateOutDirLayout verifies that legacy flat recordings folders are
// moved into <out>/<platform>/<login> on startup.
func TestMigrateOutDirLayout(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "share", "StreamDVR")
	_ = os.MkdirAll(filepath.Join(legacy, "xqc"), 0o755)
	_ = os.WriteFile(filepath.Join(legacy, "xqc", "xqc_1.ts"), make([]byte, 5), 0o644)

	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = legacy
	a.cfg.Channels = []Channel{{Name: "xqc", Login: "xqc", Platform: "twitch"}}
	a.migrateOutDirLayout()

	if _, err := os.Stat(filepath.Join(legacy, "twitch", "xqc", "xqc_1.ts")); err != nil {
		t.Fatalf("recordings must move under twitch/xqc, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "xqc")); err == nil {
		t.Fatal("legacy flat folder should no longer exist")
	}

	// migration is idempotent
	a.migrateOutDirLayout()
	if _, err := os.Stat(filepath.Join(legacy, "twitch", "xqc", "xqc_1.ts")); err != nil {
		t.Fatalf("second migrate must be a no-op, err=%v", err)
	}
}

// TestFileRename verifies the rename endpoint: the extension is preserved, the
// .idx sidecar follows, and renaming the currently growing recording is refused.
func TestFileRename(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = filepath.Join(dir, "recordings")
	folder := filepath.Join(a.cfg.OutDir, "twitch", "xqc")
	_ = os.MkdirAll(folder, 0o755)
	_ = os.WriteFile(filepath.Join(folder, "stary_klip.ts"), make([]byte, 10), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "stary_klip.ts.idx"), []byte(`[{"O":0,"S":10,"D":2}]`), 0o644)

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()

	rename := func(from, to string) string {
		body := bytes.NewBufferString(`{"to":"` + to + `"}`)
		resp, err := http.Post(ts.URL+"/api/files/twitch/xqc/"+from+"/rename", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// extension must be kept when the user types a name without it
	rename("stary_klip.ts", "nowy_klip")
	if _, err := os.Stat(filepath.Join(folder, "nowy_klip.ts")); err != nil {
		t.Fatalf("expected nowy_klip.ts, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "stary_klip.ts")); err == nil {
		t.Fatal("old name must be gone")
	}
	if _, err := os.Stat(filepath.Join(folder, "nowy_klip.ts.idx")); err != nil {
		t.Fatalf("sidecar must follow the rename, err=%v", err)
	}

	// renaming the active recording is refused
	a.recsMu.Lock()
	a.recs["twitch:xqc"] = &Recorder{app: a, login: "xqc", platform: "twitch", out: filepath.Join(folder, "nowy_klip.ts")}
	a.recsMu.Unlock()
	if b := rename("nowy_klip.ts", "zajety"); !bytes.Contains([]byte(b), []byte("trwa nagrywanie")) {
		t.Fatalf("rename of a growing recording must be refused, got %s", b)
	}
	a.recsMu.Lock()
	delete(a.recs, "twitch:xqc")
	a.recsMu.Unlock()
	if _, err := os.Stat(filepath.Join(folder, "zajety.ts")); err == nil {
		t.Fatal("growing recording must not be renamed")
	}

	// traversal is rejected
	if b := rename("nowy_klip.ts", ".."); !bytes.Contains([]byte(b), []byte("nieprawidłowa")) {
		t.Fatal("traversal rename must fail, got " + b)
	}
}

// TestFixPermissions verifies the recordings tree gets 0777 dirs and 0666
// files for all users (the FTP ownership problem).
func TestFixPermissions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rec")
	for _, p := range []string{"twitch/xqc", "kick/esport", "chaturbate/hayleex"} {
		d := filepath.Join(out, p)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "clip.ts"), make([]byte, 4), 0o600)
	}
	fixPermissions(out)

	wantDirs := []string{out, filepath.Join(out, "twitch"), filepath.Join(out, "twitch", "xqc"),
		filepath.Join(out, "kick"), filepath.Join(out, "kick", "esport"),
		filepath.Join(out, "chaturbate"), filepath.Join(out, "chaturbate", "hayleex")}
	for _, d := range wantDirs {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if st.Mode().Perm()&0o200 == 0 || st.Mode().Perm()&0o002 == 0 {
			t.Errorf("dir %s should be group/other writable (0777 target), got %o", d, st.Mode().Perm())
		}
	}
	for _, p := range []string{"twitch/xqc/clip.ts", "kick/esport/clip.ts", "chaturbate/hayleex/clip.ts"} {
		st, err := os.Stat(filepath.Join(out, p))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o002 == 0 {
			t.Errorf("file %s should be other-writable (0666 target), got %o", p, st.Mode().Perm())
		}
	}
}

// TestIdxRemovedOnFinish verifies the growing-playlist sidecar is deleted when
// a recording stops.
func TestIdxRemovedOnFinish(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		out  string
		fn   func(r *Recorder)
	}{
		{"ts", "x_2026.ts", func(r *Recorder) { r.finish(100, false) }},
	} {
		p := filepath.Join(dir, tc.out)
		_ = os.WriteFile(p, make([]byte, 8), 0o644)
		_ = os.WriteFile(p+".idx", []byte(`[{"O":0,"S":8,"D":2}]`), 0o644)
		r := &Recorder{app: a, login: "x", platform: "twitch", out: p, log: []string{}}
		tc.fn(r)
		if _, err := os.Stat(p + ".idx"); err == nil {
			t.Errorf("%s: .idx must be removed on finish", tc.name)
		}
	}
}

// roundTripFunc adapts a handler into a net/http RoundTripper for tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestManualStartKeepsPaused verifies that "Nagraj teraz" on a non-monitored
// channel keeps the paused flag (the channel stays 'nie monitorowany' while a
// one-off recording runs).
func TestManualStartKeepsPaused(t *testing.T) {
	dir := t.TempDir()
	a, err := NewApp(filepath.Join(dir, "root"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.OutDir = filepath.Join(dir, "recordings")
	a.cfg.Channels = []Channel{{Name: "hay", Login: "hayleex", Platform: "chaturbate", Paused: true}}
	a.httpc = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Status: "404 not found", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	ts := httptest.NewServer(a.setupRoutes())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/channels/hayleex/start?platform=chaturbate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(b, []byte(`"ok":true`)) {
		t.Fatalf("start should succeed, got %s", b)
	}
	a.mu.Lock()
	paused := a.cfg.Channels[0].Paused
	a.mu.Unlock()
	if !paused {
		t.Fatal("channel must stay 'nie monitorowany' after a manual record start")
	}
}
