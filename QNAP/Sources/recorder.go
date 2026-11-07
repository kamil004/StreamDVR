package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
)

var segRe = regexp.MustCompile(`#EXTINF:\s*([0-9.]+)\s*,[^\n]*\n\s*([^\n]+)`)

// segInfo records one downloaded HLS segment: its byte range in the growing
// .ts file (O=offset, S=size) and its source duration. It is persisted to a
// <file>.idx sidecar after every appended segment so the /playlist handler can
// serve the growing file as a live (EVENT) HLS stream with byte-range
// fragments aligned to the original CDN segment boundaries.
type segInfo struct {
	O int64   `json:"o"`
	S int64   `json:"s"`
	D float64 `json:"d,omitempty"`
}

func (r *Recorder) writeIdx(segs []segInfo) {
	b, err := json.Marshal(segs)
	if err != nil {
		r.push("writeIdx marshal error: %v", err)
		return
	}
	tmp := r.out + ".idx.tmp"
	if err := os.WriteFile(tmp, b, 0o666); err != nil {
		r.push("writeIdx file error: %v", err)
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, r.out+".idx"); err != nil {
		r.push("writeIdx rename error: %v", err)
		_ = os.Remove(tmp)
		return
	}
	_ = os.Chmod(r.out+".idx", 0o666)
}

// indexOf reads the sidecar index for a recorded .ts file.
func indexOf(ts string) ([]segInfo, bool) {
	b, err := os.ReadFile(ts + ".idx")
	if err != nil {
		return nil, false
	}
	var segs []segInfo
	if err := json.Unmarshal(b, &segs); err != nil || len(segs) == 0 {
		return nil, false
	}
	for i := range segs {
		if segs[i].S <= 0 || segs[i].D <= 0 {
			segs[i].D = 2
		}
	}
	return segs, true
}

// recKey is the unique recorder/status key for a channel. The same nickname
// may exist on two platforms (e.g. xqc on Twitch and on Kick), so the key is
// platform+login rather than login alone.
func recKey(platform, login string) string {
	if platform == "" {
		platform = "twitch"
	}
	return strings.ToLower(platform) + ":" + strings.ToLower(login)
}

func (c Channel) key() string {
	return recKey(c.Platform, c.Login)
}

func (r *Recorder) key() string {
	return recKey(r.platform, r.login)
}

type Recorder struct {
	app           *App
	login         string
	platform      string
	out           string
	file          *os.File
	stop          chan struct{}
	done          chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	logMu         sync.Mutex
	log           []string
	closed        uint32
	convertOnStop atomic.Bool
	converting    atomic.Bool
	startAt       time.Time
	res           string
}

func (r *Recorder) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Recorder) push(f string, a ...interface{}) {
	line := fmt.Sprintf(f, a...)
	r.logMu.Lock()
	r.log = append(r.log, line)
	if len(r.log) > 30 {
		r.log = r.log[len(r.log)-30:]
	}
	r.logMu.Unlock()
}

func (a *App) isRecording(key string) bool {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	_, ok := a.recs[key]
	return ok
}

func (a *App) isConverting(key string) bool {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	r, ok := a.recs[key]
	return ok && r.converting.Load()
}

func (a *App) recLog(key string) []string {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	if r, ok := a.recs[key]; ok {
		r.logMu.Lock()
		defer r.logMu.Unlock()
		out := make([]string, len(r.log))
		copy(out, r.log)
		return out
	}
	return nil
}

// recStart returns the unix timestamp when the current recording started
// (0 when not recording), used by the UI to show the REC timer.
func (a *App) recStart(key string) int64 {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	if r, ok := a.recs[key]; ok {
		return r.startAt.Unix()
	}
	return 0
}

// activeRecFile returns the file name being recorded right now for a channel
// ("" when nothing is recording). The files panel uses it to refuse
// converting/deleting the currently growing recording. It matches by platform
// and login because recordings are stored per platform folder.
func (a *App) activeRecFile(platform, login string) string {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	for _, r := range a.recs {
		if r.login == login && strings.EqualFold(plats(r.platform), plats(platform)) && r.out != "" {
			return filepath.Base(r.out)
		}
	}
	return ""
}

func (a *App) startRec(key, login, platform string) {
	a.recsMu.Lock()
	if _, ok := a.recs[key]; ok {
		a.recsMu.Unlock()
		return
	}
	r := &Recorder{app: a, login: login, platform: platform, stop: make(chan struct{}), done: make(chan struct{})}
	a.recs[key] = r
	a.recsMu.Unlock()

	go func() {
		defer close(r.done)
		r.run()
		a.recsMu.Lock()
		delete(a.recs, key)
		a.recsMu.Unlock()
	}()
	a.logf("recording started for %s (%s)", login, platform)
}

func (a *App) stopRec(key, reason string) {
	a.recsMu.Lock()
	r, ok := a.recs[key]
	if ok && atomic.CompareAndSwapUint32(&r.closed, 0, 1) {
		close(r.stop)
	}
	a.recsMu.Unlock()
	if ok {
		go func() {
			<-r.done
			a.logf("recording stopped for %s (%s)", key, reason)
		}()
	}
}

func (r *Recorder) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx, r.cancel = ctx, cancel
	defer cancel()

	switch r.platform {
	case "kick":
		r.runKick(ctx)
	case "chaturbate":
		r.runChaturbate(ctx)
	default:
		r.runTwitch(ctx)
	}
}

func (r *Recorder) runTwitch(ctx context.Context) {
	token := r.app.token()
	r.push("starting…")

	sig, ptok, err := playbackCtx(r.app.httpc, ctx, token, r.login)
	if err != nil {
		r.push("playback token error: %v", err)
		r.app.stopRec(r.key(), "playback error")
		return
	}
	master, err := masterPlaylistCtx(r.app.httpc, ctx, token, r.login, sig, ptok)
	if err != nil {
		r.push("master playlist error: %v", err)
		r.app.stopRec(r.key(), "master error")
		return
	}
	if strings.Contains(strings.ToLower(master), "slatetemplate") {
		r.push("warning: master contains ad (slate) variants")
	}
	media, variant, res, err := pickVariantInfo(master, resolveUsher)
	if err != nil {
		r.push("variant error: %v", err)
		r.app.stopRec(r.key(), "variant error")
		return
	}
	r.res = res
	r.push("recording variant: %s", variant)
	if res != "" {
		r.push("resolution: %s", res)
	}

	mediaURL, err := url.Parse(media)
	if err != nil {
		r.push("media URL error: %v", err)
		r.app.stopRec(r.key(), "media error")
		return
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "OAuth " + token
	}
	r.recordLoop(ctx, mediaURL, headers, func(ctx context.Context) (bool, error) {
		live, _, err := fetchLiveCtx(r.app.httpc, ctx, token, r.login)
		return live, err
	})
}

// runKick records a Kick stream directly from its public HLS. Kick authenticates
// through several session cookies (optional); the channel API gives a
// playback_url (master playlist) that requires no login.
func (r *Recorder) runKick(ctx context.Context) {
	cookie := r.app.kickToken()
	r.push("starting… (kick)")

	master, base, err := kickMaster(r.app.httpc, cookie, r.login)
	if err != nil {
		r.push("master playlist error: %v", err)
		r.app.stopRec(r.key(), "master error")
		return
	}
	media, variant, res, err := pickVariantInfo(master, func(u string) string { return resolveKick(u, base) })
	if err != nil {
		r.push("variant error: %v", err)
		r.app.stopRec(r.key(), "variant error")
		return
	}
	r.res = res
	r.push("recording variant: %s", variant)
	if res != "" {
		r.push("resolution: %s", res)
	}

	mediaURL, err := url.Parse(media)
	if err != nil {
		r.push("media URL error: %v", err)
		r.app.stopRec(r.key(), "media error")
		return
	}
	headers := kickHeaders(cookie, "https://kick.com/"+url.PathEscape(strings.ToLower(r.login)))
	r.recordLoop(ctx, mediaURL, headers, func(ctx context.Context) (bool, error) {
		live, _, err := kickLive(r.app.httpc, cookie, r.login)
		return live, err
	})
}

// runChaturbate records a Chaturbate room through the bundled ffmpeg, exactly
// like the macOS/Windows apps. Chaturbate serves LL-HLS whose master separates
// video renditions from their audio playlists (#EXT-X-MEDIA); the previous
// collector wrote the fragments itself and so recordings came out with no
// audio at all and a large base timestamp (a blank lead-in). ffmpeg demuxes the
// best video rendition and its audio playlist into one real MPEG-TS with copy
// codecs, then finish() remuxes it into a fast-start .mp4. If ffmpeg drops the
// capture mid-stream (token rotation / stream boundary) it is restarted into a
// new part file and the parts are concatenated into the single recording at the
// end, mirroring streamlink's re-open retries on macOS.
func (r *Recorder) runChaturbate(ctx context.Context) {
	a := r.app
	ff := a.ffmpegPath()
	if ff == "" {
		r.push("ffmpeg not found - cannot record chaturbate")
		a.stopRec(r.key(), "no ffmpeg")
		return
	}
	r.push("starting… (chaturbate)")
	if v, err := ffmpegVersion(ff); err != nil {
		r.push("ffmpeg version probe failed: %v", err)
	} else {
		r.push("ffmpeg: %s", v)
	}

	// a flaky room fetch must never kill a running stream
	liveCheck := func() bool {
		live, _, err := chaturbateLive(a.httpc, r.login)
		return err != nil || live
	}

	a.mu.Lock()
	outDir := filepath.Join(a.cfg.OutDir, r.platform, r.login)
	a.mu.Unlock()
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		r.push("mkdir error: %v", err)
		a.stopRec(r.key(), "mkdir error")
		return
	}
	if err := os.Chmod(outDir, 0o777); err != nil {
		r.push("chmod error on outDir: %v", err)
	}

	referer := "https://chaturbate.com/" + url.PathEscape(strings.ToLower(r.login)) + "/"

	// recordLoop sets this for twitch/kick; the chaturbate path captures through
	// ffmpeg so it must set the REC-timer anchor here or the dashboard shows a
	// timer since year 1 (time.Time zero value).
	r.startAt = time.Now()

	const maxParts = 5
	var parts []string
	for part := 1; part <= maxParts; part++ {
		if r.stopped() {
			break
		}
		if !liveCheck() {
			r.push("channel went offline")
			break
		}

		master, base, err := chaturbateMaster(a.httpc, r.login)
		if err != nil {
			r.push("master playlist error: %v", err)
			break
		}
		best, err := cbBestStreams(master, base)
		if err != nil {
			r.push("variant error: %v", err)
			break
		}
		r.res = best.res
		if best.res != "" {
			r.push("resolution: %s", best.res)
		}

		suffix := ""
		if part > 1 {
			suffix = fmt.Sprintf("_p%d", part)
		}
		out := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s%s.ts", r.login,
			time.Now().Format("2006-01-02_15-04-05"), slugTitle(r.titleFromLive()), suffix))
		r.out = out

		t0 := time.Now()
		why := r.captureChaturbateFF(ctx, ff, referer, out, best, liveCheck)
		dur := time.Since(t0)
		if why == "start" {
			a.stopRec(r.key(), "ffmpeg start failed")
			return
		}

		got := fileSize(out)
		if got < 1024 {
			if err := os.Remove(out); err != nil {
				r.push("remove discarded file error: %v", err)
			}
			r.out = ""
			r.push("discarded: captured less than 1 kB (%s)", durRound(dur))
			// a dead capture from an ffmpeg hiccup (e.g. stale token) while the
			// room is still live is retried into a fresh part
			if r.stopped() || why != "exit" || part >= maxParts || dur < cbMinCapture {
				break
			}
			continue
		}

		parts = append(parts, out)

		if r.stopped() || why != "exit" || part >= maxParts {
			break
		}
		if dur < cbMinCapture {
			r.push("stop: capture ended too early after %s - not reconnecting", durRound(dur))
			break
		}
		r.push("reconnecting… (part %d)", part+1)
	}

	if len(parts) == 0 {
		return
	}

	// concatenate parts into the first (base) part so the clicker gets one
	// single recording file regardless of how many times ffmpeg was restarted
	final := parts[0]
	got := fileSize(final)
	if len(parts) > 1 {
		r.push("merging %d parts…", len(parts))
		if n := r.concatPartsTS(ff, parts, final); n > 0 {
			for _, p := range parts[1:] {
				_ = os.Remove(p)
			}
			r.out = final
			got = n
		}
	}
	r.push("done: %.1f MB saved", float64(got)/1048576)

	// captureChaturbateFF may have pre-compensated with the live PTS probe, but
	// the finished file can still carry a leftover audio offset (segment-boundary
	// randomness, AAC priming). Measure the recording as-written and re-time the
	// audio when the drift is audible. This runs before MP4 conversion so the
	// delivered .mp4 is already in sync.
	if n := a.fixAVSync(final); n > 0 {
		r.push("av sync: audio re-timed in final file")
		got = n
	}

	convert := a.autoMP4()
	if r.stopped() {
		convert = r.convertOnStop.Load()
	}
	r.finish(got, convert)
}

// cbMinCapture is the minimum duration a finished part must have before the
// recorder will try to continue with a reconnect; a shorter exit means ffmpeg
// is broken for this stream (see the segfaulting bundled 7.0.2) and a retry
// would just produce more near-empty parts. Configurable for tests.
var cbMinCapture = 30 * time.Second

func durRound(d time.Duration) time.Duration {
	return d.Round(time.Second)
}

// concatPartsTS muxes mpegts parts back into one .ts file with ffmpeg's concat
// demuxer and writes it over dst. It returns the size of the merged file, or 0
// on failure (leaving the caller with the untouched original parts).
func (r *Recorder) concatPartsTS(ff string, parts []string, dst string) int64 {
	list := filepath.Join(filepath.Dir(dst), ".concat.txt")
	f, err := os.Create(list)
	if err != nil {
		return 0
	}
	for _, p := range parts {
		_, _ = fmt.Fprintf(f, "file '%s'\n", strings.ReplaceAll(p, "'", `'\''`))
	}
	if err = f.Close(); err != nil {
		return 0
	}
	defer os.Remove(list)

	tmp := dst + ".merge.tmp"
	cmd := exec.Command(ff, "-nostdin", "-y", "-v", "error", "-f", "concat",
		"-safe", "0", "-i", list, "-c", "copy", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			r.push("concatenate error: %s", s)
		} else {
			r.push("concatenate error: %v", err)
		}
		_ = os.Remove(tmp)
		return 0
	}
	_ = os.Remove(dst)
	_ = os.Rename(tmp, dst)
	return fileSize(dst)
}

// avSyncThreshold is the residual |audio − video| PTS offset (seconds) below
// which a recording is left alone — anything bigger gets the audio re-timed.
var avSyncThreshold = 0.25

// fixAVSync measures the A/V PTS offset of a finished .ts recording and, when
// the audio runs more than avSyncThreshold off the video timeline, remuxes it
// with the audio stream shifted back into place (fast -c copy, no re-encoding).
// It returns the size of the corrected file, or 0 when nothing was changed.
func (a *App) fixAVSync(path string) int64 {
	d, err := tsAVDelta(path)
	if err != nil {
		return 0
	}
	if d >= -avSyncThreshold && d <= avSyncThreshold {
		return 0
	}
	ff := a.ffmpegPath()
	if ff == "" {
		return 0
	}
	shift := -d
	tmp := path + ".sync.tmp"
	args := []string{
		"-nostdin", "-y", "-v", "error",
		"-copyts", "-i", path,
		"-copyts", "-itsoffset", strconv.FormatFloat(shift, 'f', 3, 64), "-i", path,
		"-map", "0:v:0", "-map", "1:a:0",
		"-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", tmp,
	}
	if err := a.runFF(ff, args...); err != nil {
		_ = os.Remove(tmp)
		a.logf("av sync fix failed for %s: %v", filepath.Base(path), err)
		return 0
	}
	d2, err2 := tsAVDelta(tmp)
	if err2 != nil || d2 < -avSyncThreshold || d2 > avSyncThreshold {
		_ = os.Remove(tmp)
		a.logf("av sync fix verification failed for %s (was %.3fs)", filepath.Base(path), d)
		return 0
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0
	}
	a.logf("av sync fixed for %s: audio shifted %.3fs", filepath.Base(path), shift)
	return fileSize(path)
}

// measureCBAVDelta captures a few seconds of the live video+audio streams and
// returns the first-PTS offset between them: (audio_first − video_first) in
// seconds, as seen with -c copy. Positive means audio starts later in the
// output than video. Errors are returned as-is so the caller can decide to
// capture without retiming.
func (r *Recorder) measureCBAVDelta(ctx context.Context, ff, referer string, best cbRendition) (float64, error) {
	tmp, err := os.CreateTemp("", "cbprobe-*.ts")
	if err != nil {
		return 0, err
	}
	probe := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(probe)

	args := []string{
		"-nostdin", "-y", "-v", "error",
		"-user_agent", chaturbateUA, "-headers", "Referer: " + referer + "\r\n",
		"-copyts", "-t", "4", "-i", best.video,
	}
	if best.audio == "" {
		args = append(args, "-map", "0:v:0", "-map", "0:a?")
	} else {
		args = append(args,
			"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
			"-copyts", "-t", "4", "-i", best.audio,
			"-map", "0:v:0", "-map", "1:a:0")
	}
	args = append(args, "-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", probe)
	cmd := exec.Command(ff, args...)
	if ca := caBundlePath(ff); ca != "" {
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+ca)
	}
	pctx, cancel := context.WithTimeout(ctx, cbProbeTimeout)
	defer cancel()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-waitCh:
	case <-pctx.Done():
		_ = cmd.Process.Kill()
		<-waitCh
		return 0, pctx.Err()
	}
	return tsAVDelta(probe)
}

// cbProbeTimeout caps how long the avsync probe may take before it is killed.
var cbProbeTimeout = 15 * time.Second

// captureChaturbateFF runs one ffmpeg capture of a chaturbate rendition into a
// .ts file. It returns why the capture ended:
//   - "start"   ffmpeg could not be started,
//   - "exit"    ffmpeg ended on its own (stream/request error, ended playlist),
//   - "manual"  the recorder was stopped,
//   - "offline" the room went offline (its liveCheck polls turned false).
//
// Manual stop and going offline first send SIGINT so ffmpeg can flush the
// muxer cleanly at the next packet boundary, with a kill fallback; a hard kill
// would leave the tail of the .ts inside a half-written packet.
func (r *Recorder) captureChaturbateFF(ctx context.Context, ff, referer, out string, best cbRendition, liveCheck func() bool) string {
	args := []string{"-nostdin", "-y", "-v", "error"}
	if best.audio != "" {
		// cb_hls_chat often starts the audio segment a beat before the video
		// segment, so the shell script used to record audio-lag of about a
		// second. Retiming the audio input to the video input is not possible
		// with -c copy alone (that would need re-encoding), so probe the live
		// streams for the first video/audio PTS offset and shift the audio
		// input back with -itsoffset.
		if delta, err := r.measureCBAVDelta(ctx, ff, referer, best); err == nil && delta != 0 {
			r.push("chaturbate: audio/video offset %.3fs, shifting audio by %.3fs", delta, -delta)
			args = append(args,
				"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
				"-copyts", "-start_at_zero", "-i", best.video,
				"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
				"-copyts", "-itsoffset", strconv.FormatFloat(-delta, 'f', 3, 64),
				"-i", best.audio,
				"-map", "0:v:0", "-map", "1:a:0")
		} else {
			args = append(args,
				"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
				"-copyts", "-start_at_zero", "-i", best.video,
				"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
				"-copyts", "-start_at_zero", "-i", best.audio,
				"-map", "0:v:0", "-map", "1:a:0")
		}
	} else {
		args = append(args,
			"-user_agent", chaturbateUA, "-headers", "Referer: "+referer+"\r\n",
			"-copyts", "-start_at_zero", "-i", best.video,
			"-map", "0:v:0", "-map", "0:a?")
	}
	args = append(args, "-c", "copy", "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", out)

	cmd := exec.Command(ff, args...)
	// The bundled static ffmpeg has no default CA store of its own, so the
	// OpenSSL-backed https protocol verifies with the system store. QNAP does
	// not always ship one, so point it at the CA bundle shipped next to the
	// binary (a small compatibility shim + a real bundle) whenever it exists.
	if ca := caBundlePath(ff); ca != "" {
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+ca)
	}
	stderr, perr := cmd.StderrPipe()
	if perr != nil {
		r.push("ffmpeg stderr: %v", perr)
		return "start"
	}
	var tail tailBuffer
	if err := cmd.Start(); err != nil {
		r.push("ffmpeg start error: %v", err)
		return "start"
	}
	go func() { _, _ = io.Copy(&tail, stderr) }()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	r.push("recording to %s", filepath.Base(out))
	why := ""
	stopped := false
	for !stopped {
		select {
		case <-r.stop:
			why, stopped = "manual", true
		case <-ctx.Done():
			why, stopped = "manual", true
		case <-waitCh:
			why = "exit"
			stopped = true
		case <-time.After(cbPollInterval):
			if !liveCheck() {
				why, stopped = "offline", true
			}
		}
	}

	if why != "exit" {
		// graceful shutdown first: ffmpeg flushes the muxer at the next packet
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGINT)
		}
		select {
		case <-waitCh:
		case <-time.After(cbStopGrace):
			_ = cmd.Process.Kill()
			<-waitCh
		}
	}
	if s := strings.TrimSpace(tail.String()); s != "" {
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				r.push("ffmpeg: %s", l)
			}
		}
	}
	return why
}

// cbPollInterval is how often the chaturbate capture re-checks that the room is
// still live while ffmpeg is running. Configurable for tests.
var cbPollInterval = 10 * time.Second

// cbStopGrace is how long a chaturbate capture waits after SIGINT before
// killing ffmpeg. Configurable for tests.
var cbStopGrace = 20 * time.Second

// tailBuffer keeps only the last 4 KiB of a child process's stderr so ffmpeg
// errors can be shown in the recorder log without buffering unbounded output.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if n := t.buf.Len() - 4096; n > 0 {
		t.buf.Next(n)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

// caBundlePath returns a CA bundle file that pairs with the given ffmpeg
// binary ("<dir of ffmpeg>/cacert.pem"), or "" when there is none. The bundled
// static ffmpeg is built with OpenSSL whose compiled-in default cert store
// lives at /etc/ssl on the build host; on QNAP that path may not hold any
// certificates, so the package ships its own bundle for verification.
func caBundlePath(ff string) string {
	ca := filepath.Join(filepath.Dir(ff), "cacert.pem")
	if st, err := os.Stat(ca); err == nil && st.Mode().IsRegular() {
		return ca
	}
	return ""
}

// ffmpegVersion returns the first line of `ffmpeg -version`. Logging it into
// the recorder log is the fastest way to tell which binary actually loaded on
// the NAS (a missing bundled binary or an incompatible glibc shows up here).
func ffmpegVersion(ff string) (string, error) {
	cmd := exec.Command(ff, "-version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
	}
	if i := strings.IndexByte(string(out), '\n'); i > 0 {
		return strings.TrimSpace(string(out)[:i]), nil
	}
	return strings.TrimSpace(string(out)), nil
}

// recordLoop downloads the segments of a live HLS media playlist until the
// stream goes offline or the recorder is stopped. The platform-specific parts
// (headers and the offline/live probe) are injected as callbacks.
func (r *Recorder) recordLoop(ctx context.Context, mediaURL *url.URL, headers map[string]string, liveCheck func(ctx context.Context) (bool, error)) {
	a := r.app
	a.mu.Lock()
	outDir := filepath.Join(a.cfg.OutDir, r.platform, r.login)
	a.mu.Unlock()
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		r.push("mkdir error: %v", err)
		r.app.stopRec(r.key(), "mkdir error")
		return
	}
	if err := os.Chmod(outDir, 0o777); err != nil {
		r.push("chmod error on outDir: %v", err)
	}

	r.out = filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.ts", r.login,
		time.Now().Format("2006-01-02_15-04-05"), slugTitle(r.titleFromLive())))
	f, err := os.Create(r.out)
	if err != nil {
		r.push("create error: %v", err)
		r.app.stopRec(r.key(), "create error")
		return
	}
	_ = f.Chmod(0o666)
	r.file = f
	r.startAt = time.Now()

	buf := make([]byte, 256*1024)
	seen := map[string]bool{}
	var got int64
	var idx []segInfo
	emptyRun := 0

	r.push("recording to %s", r.out)
	for {
		if r.stopped() {
			r.finish(got, r.convertOnStop.Load())
			return
		}
		// stop when the channel goes offline
		live, err := liveCheck(ctx)
		if err == nil && !live {
			r.finish(got, a.autoMP4())
			return
		}
		pl, err := reqCtx(a.httpc, ctx, "GET", mediaURL.String(), nil, headers)
		if err != nil {
			emptyRun++
			if r.stopped() {
				r.finish(got, r.convertOnStop.Load())
				return
			}
			if emptyRun > 4 {
				r.push("media playlist failing (%v)", err)
				r.finish(got, false)
				return
			}
			time.Sleep(4 * time.Second)
			continue
		}
		emptyRun = 0
		plText := string(pl)
		for _, m := range segRe.FindAllStringSubmatch(plText, -1) {
			if r.stopped() {
				r.finish(got, r.convertOnStop.Load())
				return
			}
			uri := strings.TrimSpace(m[2])
			// skip ad segments (Twitch 'slate' videos)
			if strings.Contains(uri, "slatetemplate") {
				if !seen[uri] {
					seen[uri] = true
					r.push("skipping ad segment")
				}
				continue
			}
			if seen[uri] {
				continue
			}
			seen[uri] = true
			segRef, perr := url.Parse(uri)
			if perr != nil {
				segRef = &url.URL{Path: uri}
			}
			var segURL *url.URL
			if segRef.IsAbs() {
				segURL = segRef
			} else {
				segURL = mediaURL.ResolveReference(segRef)
			}
			segr, rerr := http.NewRequestWithContext(ctx, "GET", segURL.String(), nil)
			if rerr != nil {
				continue
			}
			segr.Header.Set("User-Agent", ua)
			for k, v := range headers {
				segr.Header.Set(k, v)
			}
			resp, gerr := a.httpc.Do(segr)
			if gerr != nil {
				if r.stopped() {
					r.finish(got, r.convertOnStop.Load())
					return
				}
				r.push("segment error: %v", gerr)
				continue
			}
			segOff := got
			dur, _ := strconv.ParseFloat(strings.TrimSpace(m[1]), 64)
			if dur <= 0 {
				dur = 2
			}
			n, cerr := io.CopyBuffer(f, resp.Body, buf)
			resp.Body.Close()
			if cerr != nil {
				if r.stopped() {
					r.finish(got, r.convertOnStop.Load())
					return
				}
				r.push("write error: %v", cerr)
				continue
			}
			got += n
			if n > 0 {
				idx = append(idx, segInfo{O: segOff, S: n, D: dur})
				r.writeIdx(idx)
			}
			r.push("seg +%d kB (total %.1f MB)%s", n/1024, float64(got)/1048576, r.pushResSuffix())
		}
		time.Sleep(1 * time.Second)
	}
}

// pushResSuffix appends the recording resolution to a segment log line when
// it is known (e.g. "seg +946 kB (total 6.7 MB)  1280x720").
func (r *Recorder) pushResSuffix() string {
	if r.res == "" {
		return ""
	}
	return "  " + r.res
}

func (r *Recorder) titleFromLive() string {
	switch r.platform {
	case "kick":
		_, title, err := kickLive(r.app.httpc, r.app.kickToken(), r.login)
		if err != nil || title == "" {
			return "stream"
		}
		return title
	case "chaturbate":
		_, title, err := chaturbateLive(r.app.httpc, r.login)
		if err != nil || title == "" {
			return "stream"
		}
		return title
	}
	_, title, err := fetchLive(r.app.httpc, r.app.token(), r.login)
	if err != nil || title == "" {
		return "stream"
	}
	return title
}

func (r *Recorder) finish(bytes int64, convert bool) {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
		r.push("done: %.1f MB saved", float64(bytes)/1048576)
	}
	// the growing-file .idx sidecar is only needed while recording
	_ = os.Remove(r.out + ".idx")
	if !convert {
		return
	}
	if bytes <= 0 {
		r.push("nothing recorded - nothing to convert (stop during start or offline)")
		return
	}
	r.converting.Store(true)
	r.push("converting to mp4 (fast copy, no encoding)…")
	dst, err := r.app.convertMP4(r.out)
	if err != nil {
		r.push("convert error: %v", err)
	} else {
		r.push("saved: %s", dst)
	}
	r.converting.Store(false)
}

func mp4Name(ts string) string {
	return strings.TrimSuffix(ts, ".ts") + ".mp4"
}

func (a *App) autoMP4() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.AutoMP4
}

func (a *App) ffmpegPath() string {
	// test-only override wins so tests never pick up an unusable system binary
	if a.ffmpegOverride != "" {
		if st, err := os.Stat(a.ffmpegOverride); err == nil && st.Mode().IsRegular() {
			return a.ffmpegOverride
		}
	}
	// Prefer the ffmpeg bundled with the package: QNAP's own ffmpeg on PATH
	// may be missing or a newer version that behaves differently.
	candidates := []string{
		filepath.Join(a.root, "twitchdvr", "ffmpeg"),
		filepath.Join(a.root, "twitchdvr", "ffmpeg", "ffmpeg"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// convertMP4 remuxes a recorded .ts to .mp4 with ffmpeg using the same proven
// pipeline as the macOS / Windows apps:
//  1. fast copy into a temp file (+faststart), validate it is non-empty,
//  2. on failure retry with audio rewritten to AAC (video still copied),
//  3. on failure retry dropping audio (video-only mp4),
//  4. only then atomically rename over the .mp4 and remove the source .ts.
//
// The original .ts is always kept when conversion fails.
func (a *App) convertMP4(src string) (string, error) {
	ff := a.ffmpegPath()
	if ff == "" {
		return "", fmt.Errorf("ffmpeg not found - keeping original .ts")
	}
	a.logf("converting %s to mp4 using %s", filepath.Base(src), ff)

	// serialize conversions so the recorder and the "→ MP4" button never
	// race for the same temp/final file of a channel
	a.convertMu.Lock()
	defer a.convertMu.Unlock()

	final := mp4Name(src)
	tmp := final + ".part"
	// map only video+audio: Twitch recordings carry a timed_id3 data stream
	// that the MP4 container cannot take ("Could not find tag for codec timed_id3")
	attempts := [][]string{
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", tmp},
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-map", "0:a?", "-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart", "-f", "mp4", tmp},
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-c:v", "copy", "-an", "-movflags", "+faststart", "-f", "mp4", tmp},
	}
	var lastErr error
	for i, args := range attempts {
		_ = os.Remove(tmp)
		if err := a.runFF(ff, args...); err != nil {
			lastErr = err
			a.logf("convert attempt %d/%d failed: %v", i+1, len(attempts), err)
			continue
		}
		st, serr := os.Stat(tmp)
		if serr != nil || st.Size() == 0 {
			lastErr = fmt.Errorf("ffmpeg produced an empty file")
			a.logf("convert attempt %d/%d produced an empty file", i+1, len(attempts))
			continue
		}
		if err := os.Rename(tmp, final); err != nil {
			lastErr = err
			a.logf("rename to %s failed: %v", filepath.Base(final), err)
			continue
		}
		_ = os.Chmod(final, 0o666)
		_ = os.Remove(src)
		if fi, err := os.Stat(final); err == nil {
			a.logf("converted: %s (%.1f MB)", filepath.Base(final), float64(fi.Size())/1048576)
		} else {
			a.logf("converted: %s (size check failed: %v)", filepath.Base(final), err)
		}
		// verify the file is still present a moment later; something external
		// (QNAP cleanup / indexing) removing it points outside this program
		go func(finalPath string) {
			time.Sleep(2 * time.Second)
			if st, err := os.Stat(finalPath); err != nil || st.Size() == 0 {
				a.logf("WARN: %s already missing 2s after conversion (removed externally?)", filepath.Base(finalPath))
				_ = st
			}
		}(final)
		return final, nil
	}
	_ = os.Remove(tmp)
	return "", fmt.Errorf("MP4 conversion failed (%v) - kept original .ts", lastErr)
}

// finalizeLeftovers remuxes any fragmented .mp4 left on disk by a chaturbate
// recording from a previous version (versions before 2.5.8 wrote raw
// LL-HLS/fMP4, and if the app was restarted mid-stream the final remux never
// ran). Raw fMP4 files often won't start in browsers and can carry an empty
// lead-in, so they are flattened on startup. Finished flat files (no moof
// boxes) are skipped.
func (a *App) finalizeLeftovers() {
	a.mu.Lock()
	out := a.cfg.OutDir
	a.mu.Unlock()
	if out == "" {
		return
	}
	for _, plat := range []string{"twitch", "kick", "chaturbate"} {
		pdir := filepath.Join(out, plat)
		subs, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(pdir, sub.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if !strings.HasSuffix(strings.ToLower(f.Name()), ".mp4") {
					continue
				}
				p := filepath.Join(pdir, sub.Name(), f.Name())
				if !isFragmentedMP4(p) {
					continue
				}
				a.logf("finalizing leftover fragmented recording: %s", p)
				if _, err := a.remuxMP4(p); err != nil {
					a.logf("leftover remux failed for %s: %v", p, err)
				}
			}
		}
	}
}

// isFragmentedMP4 reports whether a .mp4 still contains moof boxes (a raw
// LL-HLS fMP4 before the final remux flattens it).
//
// Only the first maxProbe bytes are read: a raw LL-HLS recording always writes
// its first moof near the start (init moov is tiny, and when init is a separate
// file the recording starts straight with moof), while a finished flat file
// contains no "moof" anywhere. Reading everything would scan gigabytes of old,
// already-finalized recordings at every startup.
func isFragmentedMP4(p string) bool {
	const maxProbe = 1 << 20 // probe the first 1 MiB
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	tail := []byte{}
	read := 0
	for {
		want := min(len(buf), maxProbe-read)
		if want <= 0 {
			break
		}
		n, err := f.Read(buf[:want])
		read += n
		if n > 0 {
			chunk := append(tail, buf[:n]...)
			if bytes.Contains(chunk, []byte("moof")) {
				return true
			}
			tail = chunk[max(0, len(chunk)-3):]
		}
		if err != nil {
			break
		}
	}
	return false
}

// remuxMP4 re-boxes a fragmented .mp4 (init moov + moof/mdat, as written for
// Chaturbate LL-HLS) into a plain fast-start MP4 that plays in browsers and
// built-in players. The source file is replaced in place on success; the raw
// fragmented file is kept when the remux fails.
func (a *App) remuxMP4(src string) (string, error) {
	ff := a.ffmpegPath()
	if ff == "" {
		return "", fmt.Errorf("ffmpeg not found - keeping the fragmented file")
	}
	a.logf("remuxing %s to a plain mp4 using %s", filepath.Base(src), ff)

	// serialize alongside conversions so files of one channel never race
	a.convertMu.Lock()
	defer a.convertMu.Unlock()

	tmp := src + ".part"
	attempts := [][]string{
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", tmp},
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-map", "0:a?", "-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart", "-f", "mp4", tmp},
		{"-y", "-v", "error", "-i", src, "-map", "0:v?", "-c:v", "copy", "-an", "-movflags", "+faststart", "-f", "mp4", tmp},
	}
	var lastErr error
	for i, args := range attempts {
		_ = os.Remove(tmp)
		if err := a.runFF(ff, args...); err != nil {
			lastErr = err
			a.logf("remux attempt %d/%d failed: %v", i+1, len(attempts), err)
			continue
		}
		st, serr := os.Stat(tmp)
		if serr != nil || st.Size() == 0 {
			lastErr = fmt.Errorf("ffmpeg produced an empty file")
			a.logf("remux attempt %d/%d produced an empty file", i+1, len(attempts))
			continue
		}
		if err := os.Rename(tmp, src); err != nil {
			lastErr = err
			a.logf("rename onto %s failed: %v", filepath.Base(src), err)
			continue
		}
		_ = os.Chmod(src, 0o666)
		if fi, err := os.Stat(src); err == nil {
			a.logf("remuxed: %s (%.1f MB)", filepath.Base(src), float64(fi.Size())/1048576)
		}
		return src, nil
	}
	_ = os.Remove(tmp)
	return "", fmt.Errorf("MP4 remux failed (%v) - kept the fragmented file", lastErr)
}

func (a *App) runFF(ff string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func slugTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "stream"
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c) || c == ' ' || c == '-' || c == '_':
			b.WriteRune(c)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Join(strings.Fields(b.String()), "_")
	if len(out) > 40 {
		out = out[:40]
	}
	out = strings.Trim(out, "_")
	if out == "" {
		out = "stream"
	}
	return out
}

var _ = http.MethodGet
