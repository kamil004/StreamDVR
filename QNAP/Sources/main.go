package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/files.html
var filesHTML []byte

//go:embed web/hls.min.js
var hlsJS []byte

//go:embed web/icon.png
var iconPNG []byte

//go:embed web/twitch_favicon.png
var twitchFavicon []byte

//go:embed web/kick_favicon.png
var kickFavicon []byte

const (
	clientID    = "kimne78kx3ncx6brgo4mv6wki5h1ko"
	gqlURL      = "https://gql.twitch.tv/gql"
	usherFormat = "https://usher.ttvnw.net/api/channel/hls/%s.m3u8"
	ua          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	// appVersion is shown in the header next to the logo; keep in sync with
	// the QPKG version (build_qpkg.py / qpkg.cfg)
	appVersion = "3.0.0"
)

type Channel struct {
	Name       string   `json:"name"`
	Login      string   `json:"login"`
	Platform   string   `json:"platform,omitempty"`
	Paused     bool     `json:"paused"`
	Running    bool     `json:"running"`
	Converting bool     `json:"converting"`
	Live       bool     `json:"live"`
	Title      string   `json:"title,omitempty"`
	Avatar     string   `json:"avatar,omitempty"`
	Log        []string `json:"log"`
	StartedAt  int64    `json:"started_at,omitempty"`
}

// platform returns the normalized platform ("twitch", "kick" or "chaturbate").
func (c Channel) platform() string {
	switch c.Platform {
	case "kick", "chaturbate":
		return c.Platform
	}
	return "twitch"
}

type Config struct {
	Token        string    `json:"token"`
	Username     string    `json:"username"`
	KickToken    string    `json:"kick_token"`
	KickUsername string    `json:"kick_username"`
	OutDir       string    `json:"out_dir"`
	PollSec      int       `json:"poll_sec"`
	AutoMP4      bool      `json:"auto_mp4"`
	Channels     []Channel `json:"channels"`
	AdminPW      bool      `json:"admin_pw"`
}

type App struct {
	cfg     Config
	root    string
	dataDir string
	cfgFile string
	encFile string
	mu      sync.Mutex

	// admin-password settings encryption (guarded by mu). Only the Twitch/Kick
	// tokens/username live in the encrypted file; the recording panel itself
	// stays freely accessible.
	sKey  []byte
	sSalt []byte

	recs   map[string]*Recorder
	recsMu sync.Mutex

	// convertMu serializes ffmpeg conversions (recorder + files panel)
	convertMu sync.Mutex

	logMu  sync.Mutex
	logBuf []string

	avatars   map[string]string
	avatarTry map[string]time.Time
	avatarsMu sync.Mutex

	// anti-spam: counts unlocking attempts per browser (see /api/settings/tap).
	// Persisted to rate.json so blocks survive restarts.
	rateMu    sync.Mutex
	rateTries map[string]*rateState
	rateFile  string

	// test-only override for the ffmpeg binary used by ffmpegPath (the bundled
	// one may be an untestable foreign-platform binary on CI/development hosts)
	ffmpegOverride string

	httpc *http.Client
}

// rateState tracks settings-unlock tap attempts for one browser client.
type rateState struct {
	Tries        int       `json:"tries"`
	LastTap      time.Time `json:"last_tap"`
	BlockedUntil time.Time `json:"blocked_until"`
}

const (
	maxOpenTaps = 5               // taps before the block kicks in
	rateWindow  = 5 * time.Minute // taps must fall within this window to count
	rateBlock   = 10 * time.Minute
)

func (a *App) loadRate() {
	b, err := os.ReadFile(a.rateFile)
	if err != nil {
		return
	}
	var v map[string]*rateState
	if json.Unmarshal(b, &v) == nil {
		a.rateTries = v
	}
}

func (a *App) saveRate() {
	b, _ := json.Marshal(a.rateTries)
	_ = os.WriteFile(a.rateFile, b, 0o600)
}

// rateAttempt records one failed settings-open/unlock attempt for a client.
// Returns the state after counting; the caller checks st.BlockedUntil.
func (a *App) rateAttempt(client string) rateState {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	st := a.rateTries[client]
	if st == nil {
		st = &rateState{}
	}
	now := time.Now()
	if st.BlockedUntil.After(now) {
		return *st
	}
	if !st.LastTap.IsZero() && now.Sub(st.LastTap) > rateWindow {
		st.Tries = 0
	}
	st.LastTap = now
	st.Tries++
	if st.Tries >= maxOpenTaps {
		st.BlockedUntil = now.Add(rateBlock)
		st.Tries = 0
	}
	a.rateTries[client] = st
	a.saveRate()
	return *st
}

// rateClear forgets past attempts after a successful unlock.
func (a *App) rateClear(client string) {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	delete(a.rateTries, client)
	a.saveRate()
}

func (a *App) rateBlocked(client string) time.Time {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	if st := a.rateTries[client]; st != nil && st.BlockedUntil.After(time.Now()) {
		return st.BlockedUntil
	}
	return time.Time{}
}

func NewApp(root, dataDir string) (*App, error) {
	a := &App{
		root:      root,
		dataDir:   dataDir,
		cfgFile:   filepath.Join(dataDir, "config.json"),
		encFile:   filepath.Join(dataDir, "settings.enc"),
		recs:      map[string]*Recorder{},
		avatars:   map[string]string{},
		avatarTry: map[string]time.Time{},
		httpc:     &http.Client{Timeout: 30 * time.Second},
		rateTries: map[string]*rateState{},
		rateFile:  filepath.Join(dataDir, "rate.json"),
	}
	a.loadRate()
	a.cfg.OutDir = "/share/Public/StreamDVR"
	a.cfg.PollSec = 60
	a.cfg.AutoMP4 = true
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(a.cfgFile); err == nil {
		_ = json.Unmarshal(b, &a.cfg)
	}
	if a.cfg.AdminPW {
		// Settings are password-protected: the Twitch/Kick tokens are only
		// loaded after the password is entered in the Settings panel.
		// Monitoring and recording keep working regardless (anonymous
		// playback).
		a.cfg.Token, a.cfg.Username = "", ""
		a.cfg.KickToken, a.cfg.KickUsername = "", ""
	}
	a.migrateOutDirLayout()
	// Finalizing leftover fragmented recordings reads (and possibly remuxes)
	// files in the recordings tree. Run it in the background so the web panel
	// binds and serves immediately, even on a NAS with many recordings.
	// Clear logs on startup to avoid carrying stale entries from previous runs
	a.logBuf = a.logBuf[:0]
	a.logf("StreamDVR v%s starting", appVersion)
	go a.finalizeLeftovers()
	fixPermissions(a.cfg.OutDir)
	return a, nil
}

// migrateOutDirLayout moves recordings from the legacy flat layout
// (<out>/<login>) into the per-platform hierarchy (<out>/<platform>/<login>),
// based on the currently configured channels. It is a no-op when the target
// folder already exists.
func (a *App) migrateOutDirLayout() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ch := range a.cfg.Channels {
		plat := ch.platform()
		old := filepath.Join(a.cfg.OutDir, strings.ToLower(ch.Login))
		neu := filepath.Join(a.cfg.OutDir, plat, strings.ToLower(ch.Login))
		if old == neu {
			continue
		}
		if _, err := os.Lstat(old); err != nil {
			continue
		}
		if _, err := os.Lstat(neu); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(neu), 0o777); err != nil {
			continue
		}
		_ = os.Chmod(filepath.Dir(neu), 0o777)
		if err := os.Rename(old, neu); err == nil {
			a.logf("moved recordings %s -> %s", old, neu)
		}
	}
}

func (a *App) save() {
	a.mu.Lock()
	cfg := a.cfg
	hasSettings := a.cfg.AdminPW && a.sKey != nil
	hasPw := a.cfg.AdminPW
	key, salt := a.sKey, a.sSalt
	a.mu.Unlock()

	if hasSettings {
		sec, _ := json.Marshal(map[string]string{
			"token":         cfg.Token,
			"username":      cfg.Username,
			"kick_token":    cfg.KickToken,
			"kick_username": cfg.KickUsername,
		})
		enc, err := pack(sec, key, salt)
		if err == nil {
			_ = os.WriteFile(a.encFile, enc, 0o600)
		}
	}
	if hasPw {
		// the Twitch/Kick tokens never land in config.json once a password is set
		cfg.Token, cfg.Username = "", ""
		cfg.KickToken, cfg.KickUsername = "", ""
	}
	b, _ := json.MarshalIndent(&cfg, "", "  ")
	_ = os.WriteFile(a.cfgFile, b, 0o600)
}

// settingsLocked reports whether the Settings panel is password-protected and
// currently locked. Monitoring and recording always keep working.
func (a *App) settingsLocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.AdminPW && a.sKey == nil
}

// unlockSettings verifies the admin password against the encrypted settings
// and loads the Twitch token into memory.
func (a *App) unlockSettings(password string) error {
	a.mu.Lock()
	hasPw := a.cfg.AdminPW
	if a.sKey != nil {
		a.mu.Unlock()
		return nil // already unlocked
	}
	a.mu.Unlock()
	if !hasPw {
		return fmt.Errorf("no admin password is set yet")
	}
	enc, err := os.ReadFile(a.encFile)
	if err != nil {
		return fmt.Errorf("no encrypted settings file found")
	}
	salt := blobSalt(enc)
	key, kerr := deriveKey(password, salt)
	if kerr != nil {
		return kerr
	}
	blob, err := unpack(enc, key, salt)
	if err != nil {
		return err
	}
	var sec struct {
		Token        string `json:"token"`
		Username     string `json:"username"`
		KickToken    string `json:"kick_token"`
		KickUsername string `json:"kick_username"`
	}
	if err := json.Unmarshal(blob, &sec); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Token, a.cfg.Username = sec.Token, sec.Username
	a.cfg.KickToken, a.cfg.KickUsername = sec.KickToken, sec.KickUsername
	a.sKey, a.sSalt = key, salt
	a.mu.Unlock()
	a.logf("settings unlocked")
	return nil
}

func (a *App) checkSettingsPassword(current string) bool {
	a.mu.Lock()
	hasPw := a.cfg.AdminPW
	a.mu.Unlock()
	if !hasPw {
		return true
	}
	enc, err := os.ReadFile(a.encFile)
	if err != nil {
		return false
	}
	salt := blobSalt(enc)
	key, kerr := deriveKey(current, salt)
	if kerr != nil {
		return false
	}
	_, err = unpack(enc, key, salt)
	return err == nil
}

// setPassword sets (or changes) the admin password and starts encrypting the
// Twitch token into settings.enc. The password itself is never stored.
func (a *App) setPassword(current, password string) error {
	if len(password) < 4 {
		return fmt.Errorf("password must be at least 4 characters")
	}
	if a.cfg.AdminPW && !a.checkSettingsPassword(current) {
		return fmt.Errorf("current password is wrong")
	}
	key, salt, err := storeKey(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.sKey, a.sSalt = key, salt
	a.cfg.AdminPW = true
	a.mu.Unlock()
	a.save()
	a.logf("admin password set, Twitch/Kick tokens encrypted (AES-256-GCM)")
	return nil
}

// lockSettings drops the in-memory decryption key again; the encrypted file
// stays on disk. The Twitch/Kick tokens stay in memory so anonymized/anonymous
// recording keeps working and the header still shows the login state; editing
// the Settings is what requires the password again.
func (a *App) lockSettings() {
	a.mu.Lock()
	a.sKey, a.sSalt = nil, nil
	a.mu.Unlock()
	a.logf("settings locked")
}

func (a *App) stopRecordings(reason string) {
	a.recsMu.Lock()
	logins := make([]string, 0, len(a.recs))
	for l := range a.recs {
		logins = append(logins, l)
	}
	a.recsMu.Unlock()
	for _, l := range logins {
		a.stopRec(l, reason)
	}
}

func (a *App) logf(format string, args ...interface{}) {
	line := fmt.Sprintf(time.Now().Format("15:04:05")+" "+format, args...)
	a.logMu.Lock()
	a.logBuf = append(a.logBuf, line)
	if len(a.logBuf) > 400 {
		a.logBuf = a.logBuf[len(a.logBuf)-400:]
	}
	out := a.logBuf[len(a.logBuf)-1]
	a.logMu.Unlock()
	log.Println(out)
}

// loginFromInput parses a channel name or URL into (login, platform).
// The platform is only set when the input is clearly a Kick or Chaturbate URL;
// plain nicknames and Twitch URLs leave it empty and the caller falls back to
// the platform chosen in the UI.
func loginFromInput(s string) (string, string) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	platform := ""
	switch {
	case strings.Contains(low, "kick.com"), strings.Contains(low, "kick.tv"):
		platform = "kick"
	case strings.Contains(low, "chaturbate.com"), strings.Contains(low, "chaturbate.tv"):
		platform = "chaturbate"
	}
	s = strings.Replace(s, "https://www.", "", 1)
	for _, p := range []string{"https://", "http://", "www.", "m.", "kick.com/", "kick.tv/", "twitch.tv/", "chaturbate.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(s, "twitch.tv/")
	s = strings.TrimPrefix(s, "kick.com/")
	s = strings.TrimPrefix(s, "kick.tv/")
	s = strings.TrimPrefix(s, "chaturbate.com/")
	s = strings.SplitN(s, "/", 2)[0]
	s = strings.SplitN(s, "?", 2)[0]
	s = strings.SplitN(s, "#", 2)[0]
	return strings.ToLower(strings.Trim(s, "/")), platform
}

// findChannel returns the index of the channel with the given login AND
// platform (the same nickname may exist on both Twitch and Kick).
func (a *App) findChannel(login, platform string) int {
	for i := range a.cfg.Channels {
		if a.cfg.Channels[i].Login == login && a.cfg.Channels[i].platform() == platform {
			return i
		}
	}
	return -1
}

func (a *App) liveInfo(ch Channel) (bool, string) {
	switch ch.platform() {
	case "kick":
		live, title, err := kickLive(a.httpc, a.kickToken(), ch.Login)
		if err != nil {
			a.logf("status %s: %v", ch.Login, err)
			return false, ""
		}
		return live, title
	case "chaturbate":
		live, title, err := chaturbateLive(a.httpc, ch.Login)
		if err != nil {
			a.logf("status %s: %v", ch.Login, err)
			return false, ""
		}
		return live, title
	}
	live, title, err := fetchLive(a.httpc, a.token(), ch.Login)
	if err != nil {
		a.logf("status %s: %v", ch.Login, err)
		return false, ""
	}
	return live, title
}

func (a *App) token() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Token
}

func (a *App) kickToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.KickToken
}

// monitor periodically refreshes live status and starts/stops recordings.
func (a *App) monitor() {
	time.Sleep(2 * time.Second)
	for {
		a.mu.Lock()
		chs := make([]Channel, len(a.cfg.Channels))
		copy(chs, a.cfg.Channels)
		poll := a.cfg.PollSec
		a.mu.Unlock()
		if poll <= 0 {
			poll = 60
		}

		for i := range chs {
			ch := &chs[i]
			ch.Login = strings.ToLower(ch.Login)
			live, title := a.liveInfo(*ch)
			a.mu.Lock()
			if i < len(a.cfg.Channels) {
				if live {
					a.cfg.Channels[i].Live = true
					a.cfg.Channels[i].Title = title
				} else {
					a.cfg.Channels[i].Live = false
				}
				paused := a.cfg.Channels[i].Paused
				a.mu.Unlock()
				key := ch.key()
				if a.avatarOf(ch.Login) == "" {
					go a.fetchAvatar(ch.Login, ch.platform())
				}
				if paused {
					// non-monitored channels still get their live status
					// refreshed every cycle, but are never auto (re)started or
					// stopped; a recording began manually keeps running.
					continue
				}
				if live && !a.isRecording(key) {
					go a.startRec(key, ch.Login, ch.platform())
				}
				if !live && a.isRecording(key) {
					a.stopRec(key, "offline")
				}
			} else {
				a.mu.Unlock()
			}
		}
		time.Sleep(time.Duration(poll) * time.Second)
	}
}

// clientID returns a stable client identifier stored in a cookie. This allows
// the anti-spam logic to track settings-open attempts per-browser.
func (a *App) clientFrom(r *http.Request) string {
	// prefer the persistent per-browser id sent by the frontend (localStorage)
	var body struct {
		Client string `json:"client"`
	}
	if b, err := io.ReadAll(r.Body); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		if body.Client != "" {
			return body.Client
		}
	}
	if c, err := r.Cookie("client_id"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func (a *App) clientID(w http.ResponseWriter, r *http.Request) string {
	if id := a.clientFrom(r); id != "" {
		return id
	}
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	id := hex.EncodeToString(buf[:])
	http.SetCookie(w, &http.Cookie{
		Name:     "client_id",
		Value:    id,
		Path:     "/",
		MaxAge:   3600 * 24 * 365 * 5,
		SameSite: http.SameSiteLaxMode,
	})
	return id
}

// apiStatus builds the status object for the UI. The recording panel is always
// visible; only login/token info is hidden while the Settings are locked.
func (a *App) apiStatus() map[string]interface{} {
	locked := a.settingsLocked()
	a.mu.Lock()
	pwSet := a.cfg.AdminPW
	chans := make([]Channel, len(a.cfg.Channels))
	copy(chans, a.cfg.Channels)
	username := a.cfg.Username
	hasToken := a.cfg.Token != ""
	kickUsername := a.cfg.KickUsername
	hasKick := a.cfg.KickToken != ""
	outDir := a.cfg.OutDir
	poll := a.cfg.PollSec
	autoMP4 := a.cfg.AutoMP4
	a.mu.Unlock()

	base := map[string]interface{}{
		"settings_locked": locked,
		"admin_pw":        pwSet,
		"version":         appVersion,
		"out_dir":         outDir,
		"poll_sec":        poll,
		"auto_mp4":        autoMP4,
		"ffmpeg":          a.ffmpegPath() != "",
		"username":        username,
		"logged_in":       hasToken,
		"kick_username":   kickUsername,
		"kick_logged_in":  hasKick,
		"disk_free":       a.diskFree(outDir),
	}

	sort.Slice(chans, func(x, y int) bool {
		lx, ly := chans[x].Live, chans[y].Live
		if lx != ly {
			return lx
		}
		return chans[x].Login < chans[y].Login
	})
	for i := range chans {
		key := chans[i].key()
		chans[i].Running = a.isRecording(key)
		chans[i].Converting = a.isConverting(key)
		chans[i].StartedAt = a.recStart(key)
		chans[i].Log = a.recLog(key)
		chans[i].Avatar = a.avatarOf(chans[i].Login)
	}
	base["channels"] = chans
	return base
}

// avatarOf returns a cached avatar URL for a channel (empty if unknown yet).
func (a *App) avatarOf(login string) string {
	a.avatarsMu.Lock()
	defer a.avatarsMu.Unlock()
	return a.avatars[login]
}

// fetchAvatar resolves and caches a channel avatar. It is rate limited to one
// upstream query per channel per 15 minutes, so it is safe to call from the
// monitor loop.
func (a *App) fetchAvatar(login, platform string) {
	a.avatarsMu.Lock()
	if t, ok := a.avatarTry[login]; ok && time.Since(t) < 15*time.Minute {
		a.avatarsMu.Unlock()
		return
	}
	a.avatarTry[login] = time.Now()
	a.avatarsMu.Unlock()

	if platform == "kick" {
		pic := kickAvatar(a.httpc, a.kickToken(), login)
		if pic != "" {
			a.avatarsMu.Lock()
			a.avatars[login] = pic
			a.avatarsMu.Unlock()
		}
		return
	}
	if platform == "chaturbate" {
		pic := chaturbateAvatar(a.httpc, login)
		if pic != "" {
			a.avatarsMu.Lock()
			a.avatars[login] = pic
			a.avatarsMu.Unlock()
		}
		return
	}
	info, err := fetchUserInfo(a.httpc, login)
	if err != nil {
		a.logf("avatar %s: %v", login, err)
		return
	}
	a.avatarsMu.Lock()
	if info != nil {
		a.avatars[login] = info["profileImageURL"]
	}
	a.avatarsMu.Unlock()
}

// guardSettings rejects Settings-panel operations while the admin password
// lock is active. The recording panel itself is always available.
func (a *App) guardSettings(w http.ResponseWriter) bool {
	if a.settingsLocked() {
		writeErr(w, "settings locked - enter the admin password in the Settings panel")
		return true
	}
	return false
}

func (a *App) setupRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(iconPNG)
	})
	mux.HandleFunc("GET /twitch_favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(twitchFavicon)
	})
	mux.HandleFunc("GET /kick_favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(kickFavicon)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.apiStatus())
	})
	mux.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		a.logMu.Lock()
		lines := make([]string, len(a.logBuf))
		copy(lines, a.logBuf)
		a.logMu.Unlock()
		writeJSON(w, map[string]interface{}{"lines": lines})
	})

	// ---- media browser / web player (works without the admin password) ----
	mux.HandleFunc("GET /media/{platform}/{login}/{name}", a.handleMediaServe)
	mux.HandleFunc("GET /playlist/{platform}/{login}/{name}", a.handlePlaylist)
	mux.HandleFunc("GET /api/files/{platform}/{login}", a.handleFileList)
	mux.HandleFunc("DELETE /api/files/{platform}/{login}/{name}", a.handleFileDelete)
	mux.HandleFunc("DELETE /api/files/{platform}/{login}", a.handleFileClear)
	mux.HandleFunc("POST /api/files/{platform}/{login}/{name}/convert", a.handleFileConvert)
	mux.HandleFunc("POST /api/files/{platform}/{login}/{name}/rename", a.handleFileRename)
	mux.HandleFunc("GET /files/{platform}/{login}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(filesHTML)
	})
	mux.HandleFunc("GET /hls.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(hlsJS)
	})

	// ---- admin password (Settings panel only; token stored in settings.enc) ----
	mux.HandleFunc("POST /api/unlock", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
			Client   string `json:"client"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "bad request")
			return
		}
		id := body.Client
		if id == "" {
			id = a.clientFrom(r)
		}
		if a.settingsLocked() {
			if st := a.rateTries[id]; st != nil && st.BlockedUntil.After(time.Now()) {
				remaining := int(time.Until(st.BlockedUntil).Minutes()) + 1
				writeJSON(w, map[string]any{"error": fmt.Sprintf("Zbyt wiele prób. Dostęp zablokowany na %d min.", remaining)})
				return
			}
			if err := a.unlockSettings(body.Password); err != nil {
				a.rateAttempt(id)
				writeErr(w, err.Error())
				return
			}
			a.rateClear(id)
		}
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/settings/tap", func(w http.ResponseWriter, r *http.Request) {
		if a.settingsLocked() {
			id := a.clientID(w, r)
			st := a.rateAttempt(id)
			if st.BlockedUntil.After(time.Now()) {
				remaining := int(time.Until(st.BlockedUntil).Minutes()) + 1
				writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("Zbyt wiele prób. Dostęp zablokowany na %d min.", remaining), "until": st.BlockedUntil.Unix()})
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/settings/clear", func(w http.ResponseWriter, r *http.Request) {
		id := a.clientID(w, r)
		a.rateClear(id)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/pwset", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Current  string `json:"current"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "bad request")
			return
		}
		if a.settingsLocked() {
			writeErr(w, "settings locked - unlock first to change the password")
			return
		}
		if err := a.setPassword(body.Current, body.Password); err != nil {
			writeErr(w, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/lock", func(w http.ResponseWriter, r *http.Request) {
		a.lockSettings()
		writeJSON(w, map[string]bool{"ok": true})
	})

	// ---- Settings-panel guarded actions ----
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.handleLogin(w, r)
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.mu.Lock()
		a.cfg.Token, a.cfg.Username = "", ""
		a.mu.Unlock()
		a.save()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/kick/login", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.handleKickLogin(w, r)
	})
	mux.HandleFunc("POST /api/kick/logout", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.mu.Lock()
		a.cfg.KickToken, a.cfg.KickUsername = "", ""
		a.mu.Unlock()
		a.save()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/outdir", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.handleOutDir(w, r)
	})
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.handleExport(w, r)
	})
	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		if a.guardSettings(w) {
			return
		}
		a.handleImport(w, r)
	})

	// ---- free recording-panel actions (no password required) ----
	mux.HandleFunc("POST /api/channels", func(w http.ResponseWriter, r *http.Request) {
		a.handleAddChannel(w, r)
	})
	mux.HandleFunc("DELETE /api/channels/{login}", func(w http.ResponseWriter, r *http.Request) {
		a.handleDelChannel(w, r)
	})
	mux.HandleFunc("POST /api/channels/{login}/toggle", func(w http.ResponseWriter, r *http.Request) {
		a.handleToggleChannel(w, r)
	})
	mux.HandleFunc("POST /api/channels/{login}/start", func(w http.ResponseWriter, r *http.Request) {
		login := strings.ToLower(r.PathValue("login"))
		platform := queryPlatform(r)
		key := recKey(platform, login)
		if a.isRecording(key) {
			writeErr(w, "already recording")
			return
		}
		// a manual "Nagraj teraz" on a non-monitored channel starts a one-off
		// recording but must NOT clear the "nie monitorowany" flag: the channel
		// stays paused so the monitor never auto-starts/stops it afterwards.
		go a.startRec(key, login, platform)
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/channels/{login}/stop", func(w http.ResponseWriter, r *http.Request) {
		login := strings.ToLower(r.PathValue("login"))
		a.stopRec(recKey(queryPlatform(r), login), "manual stop")
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/channels/{login}/convert", func(w http.ResponseWriter, r *http.Request) {
		login := strings.ToLower(r.PathValue("login"))
		a.markConvert(recKey(queryPlatform(r), login))
		a.stopRec(recKey(queryPlatform(r), login), "stop + convert")
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/poll", func(w http.ResponseWriter, r *http.Request) {
		a.handlePoll(w, r)
	})
	return mux
}

// queryPlatform returns the platform requested for a channel action
// (defaults to "twitch" when the UI did not send one).
func queryPlatform(r *http.Request) string {
	p := r.URL.Query().Get("platform")
	if p != "kick" && p != "chaturbate" {
		p = "twitch"
	}
	return p
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "bad request")
		return
	}
	username, err := validateToken(a.httpc, body.Token)
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	a.mu.Lock()
	a.cfg.Token, a.cfg.Username = body.Token, username
	a.mu.Unlock()
	a.save()
	a.logf("Twitch login: %s", username)
	writeJSON(w, map[string]interface{}{"ok": true, "username": username})
}

func (a *App) handleKickLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "bad request")
		return
	}
	username, err := kickValidate(a.httpc, body.Token)
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	a.mu.Lock()
	a.cfg.KickToken, a.cfg.KickUsername = body.Token, username
	a.mu.Unlock()
	a.save()
	a.logf("Kick login: %s", username)
	writeJSON(w, map[string]interface{}{"ok": true, "username": username})
}

func (a *App) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, "channel name required")
		return
	}
	login, platform := loginFromInput(body.Name)
	// a Kick URL in the input wins; otherwise use the platform chosen in the UI
	if platform == "" {
		platform = body.Platform
	}
	if platform == "" {
		platform = "twitch"
	}
	if login == "" {
		writeErr(w, "could not parse a channel name")
		return
	}
	a.mu.Lock()
	if a.findChannel(login, platform) >= 0 {
		a.mu.Unlock()
		writeErr(w, "channel already added on this platform")
		return
	}
	a.cfg.Channels = append(a.cfg.Channels, Channel{Name: body.Name, Login: login, Platform: platform})
	a.mu.Unlock()
	a.save()
	a.logf("added channel %s (%s)", login, platform)
	go a.fetchAvatar(login, platform)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleDelChannel(w http.ResponseWriter, r *http.Request) {
	login := strings.ToLower(r.PathValue("login"))
	platform := queryPlatform(r)
	a.stopRec(recKey(platform, login), "channel removed")
	a.mu.Lock()
	idx := a.findChannel(login, platform)
	if idx >= 0 {
		a.cfg.Channels = append(a.cfg.Channels[:idx], a.cfg.Channels[idx+1:]...)
	}
	a.mu.Unlock()
	a.save()
	a.logf("removed channel %s (%s)", login, platform)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleToggleChannel(w http.ResponseWriter, r *http.Request) {
	login := strings.ToLower(r.PathValue("login"))
	platform := queryPlatform(r)
	a.mu.Lock()
	idx := a.findChannel(login, platform)
	if idx < 0 {
		a.mu.Unlock()
		writeErr(w, "no such channel")
		return
	}
	a.cfg.Channels[idx].Paused = !a.cfg.Channels[idx].Paused
	paused := a.cfg.Channels[idx].Paused
	a.mu.Unlock()
	a.save()
	// pausing only stops the monitor from (re)starting this channel; a
	// recording that is already running keeps going until stopped manually
	writeJSON(w, map[string]bool{"ok": true, "paused": paused})
}

func (a *App) handleOutDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeErr(w, "path required")
		return
	}
	p := strings.TrimRight(strings.TrimSpace(body.Path), "/")
	if err := os.MkdirAll(p, 0o777); err != nil {
		writeErr(w, "cannot create folder: "+err.Error())
		return
	}
	a.mu.Lock()
	a.cfg.OutDir = p
	a.mu.Unlock()
	a.save()
	fixPermissions(p)
	a.logf("output folder changed to %s", p)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleExport writes the full settings (channels, login tokens, folder) as a
// JSON file the user can back up or move to another machine.
func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	out := map[string]interface{}{
		"app":           "StreamDVR",
		"version":       appVersion,
		"exported_at":   time.Now().Format(time.RFC3339),
		"token":         cfg.Token,
		"username":      cfg.Username,
		"kick_token":    cfg.KickToken,
		"kick_username": cfg.KickUsername,
		"out_dir":       cfg.OutDir,
		"poll_sec":      cfg.PollSec,
		"auto_mp4":      cfg.AutoMP4,
		"channels":      cfg.Channels,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		writeErr(w, "export failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="streamdvr-settings.json"`)
	_, _ = w.Write(b)
}

// handleImport accepts a file produced by handleExport and applies its
// channels, tokens, poll interval and output folder.
func (a *App) handleImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token        string    `json:"token"`
		Username     string    `json:"username"`
		KickToken    string    `json:"kick_token"`
		KickUsername string    `json:"kick_username"`
		OutDir       string    `json:"out_dir"`
		PollSec      int       `json:"poll_sec"`
		AutoMP4      *bool     `json:"auto_mp4"`
		Channels     []Channel `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "bad import file")
		return
	}
	// never import channels without a normalized login/platform
	chs := make([]Channel, 0, len(body.Channels))
	for _, c := range body.Channels {
		if strings.TrimSpace(c.Login) == "" {
			continue
		}
		c.Login = strings.ToLower(strings.TrimSpace(c.Login))
		c.Name = strings.TrimSpace(c.Name)
		if c.Platform != "kick" && c.Platform != "chaturbate" {
			c.Platform = "twitch"
		}
		chs = append(chs, c)
	}
	a.stopRecordings("settings import")
	a.mu.Lock()
	a.cfg.Token = strings.TrimSpace(body.Token)
	a.cfg.Username = body.Username
	a.cfg.KickToken = strings.TrimSpace(body.KickToken)
	a.cfg.KickUsername = body.KickUsername
	if p := strings.TrimSpace(body.OutDir); p != "" {
		a.cfg.OutDir = strings.TrimRight(p, "/")
	}
	if body.PollSec > 0 {
		a.cfg.PollSec = body.PollSec
	}
	if body.AutoMP4 != nil {
		a.cfg.AutoMP4 = *body.AutoMP4
	}
	a.cfg.Channels = chs
	a.mu.Unlock()
	a.save()
	a.logf("settings imported: %d channels, out_dir=%s", len(chs), a.cfg.OutDir)
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePoll sets how often channels are checked for live status.
// Allowed: 15, 30, 60, 120 seconds.
func (a *App) handlePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Sec int `json:"sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "bad request")
		return
	}
	switch body.Sec {
	case 15, 30, 60, 120:
	default:
		writeErr(w, "poll interval must be one of: 15, 30, 60, 120")
		return
	}
	a.mu.Lock()
	a.cfg.PollSec = body.Sec
	a.mu.Unlock()
	a.save()
	a.logf("monitoring interval set to %ds", body.Sec)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) markConvert(key string) {
	a.recsMu.Lock()
	defer a.recsMu.Unlock()
	if r, ok := a.recs[key]; ok {
		r.convertOnStop.Store(true)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// fixPermissions makes the recordings tree (and everything under it) writable
// and deletable by every user: 0777 on directories (deletion needs write on the
// parent folder) and 0666 on files (modification needs write on the file). The
// QPKG service often runs as one system account while the owner browses the
// same folder over FTP as another account; without these bits the FTP user
// cannot delete recordings. It only touches the known three-platform folders.
func fixPermissions(out string) {
	if out == "" {
		return
	}
	for _, plat := range []string{"twitch", "kick", "chaturbate"} {
		pdir := filepath.Join(out, plat)
		_ = os.MkdirAll(pdir, 0o777)
		_ = os.Chmod(pdir, 0o777)
		subs, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, s := range subs {
			full := filepath.Join(pdir, s.Name())
			if !s.IsDir() {
				_ = os.Chmod(full, 0o666)
				continue
			}
			_ = os.Chmod(full, 0o777)
			if files, err := os.ReadDir(full); err == nil {
				for _, f := range files {
					_ = os.Chmod(filepath.Join(full, f.Name()), 0o666)
				}
			}
		}
	}
	_ = os.Chmod(out, 0o777)
}

// chmodWritable broadens a newly created recording side (file or directory) so
// any FTP user may rename/modify/delete it, regardless of the service's umask.
func chmodWritable(p string) {
	_ = os.Chmod(p, 0o666)
}

// recordingDir returns <out_dir>/<platform>/<login>, i.e. the folder where the
// recorder writes the files for a channel.
func (a *App) recordingDir(platform, login string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return filepath.Join(a.cfg.OutDir, plats(platform), strings.ToLower(login))
}

// plats normalizes a platform route value to the canonical lowercase folder
// identifier used for recording paths.
func plats(p string) string {
	switch strings.ToLower(p) {
	case "kick", "chaturbate":
		return strings.ToLower(p)
	}
	return "twitch"
}

// diskFree reports free space for the current recording folder (walks up the
// path until a mounted filesystem is found).
func (a *App) diskFree(dir string) int64 {
	a.mu.Lock()
	out := a.cfg.OutDir
	a.mu.Unlock()
	if out == "" {
		out = dir
	}
	for p := out; p != "" && p != "/"; p = parentOf(p) {
		if n, err := diskFreeBytes(p); err == nil {
			return n
		}
	}
	return 0
}

func parentOf(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// mediaPath resolves a channel file for serving and returns its absolute path.
func (a *App) mediaPath(platform, login, name string) (string, error) {
	dir := a.recordingDir(platform, login)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == ".." || strings.Contains(name, "/") {
		return "", fmt.Errorf("invalid file name")
	}
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid file name")
	}
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func (a *App) handleMediaServe(w http.ResponseWriter, r *http.Request) {
	p, err := a.mediaPath(r.PathValue("platform"), r.PathValue("login"), r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

// handlePlaylist serves an HLS playlist for a .ts recording.
//
// When the recorder wrote a per-segment .idx sidecar (every downloaded CDN
// segment is byte-aligned with the growing file), the playlist is emitted as a
// LIVE/EVENT stream: each fragment is a BYTERANGE over the same media file,
// aligned to the original segment boundaries. hls.js keeps re-polling this
// playlist and buffers newly appended segments, so an in-progress recording
// keeps playing while it grows. Finished files get an ENDLIST so the player
// plays to the end. Files without an index fall back to a single-segment VOD
// playlist.
func (a *App) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	login := strings.ToLower(r.PathValue("login"))
	name := filepath.Base(strings.ReplaceAll(r.PathValue("name"), "\\", "/"))
	if name == "" || filepath.Ext(name) != ".ts" {
		writeErr(w, "playlist is only available for .ts files")
		return
	}
	p, err := a.mediaPath(platform, login, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() <= 0 {
		http.NotFound(w, r)
		return
	}
	segs, hasIdx := indexOf(p)
	if !hasIdx {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:6.0,\n/media/%s/%s/%s\n#EXT-X-ENDLIST\n", platform, login, name)
		return
	}

	// growing only while this exact file is the active recording
	growing := a.activeRecFile(platform, login) == name
	maxDur := 2.0
	lines := []string{}
	for i := range segs {
		s := segs[i]
		if s.O+s.S > st.Size() {
			if growing {
				continue // tail segment still being written
			}
			s.S = st.Size() - s.O // clamp the last byte range to what exists
			if s.S <= 0 {
				continue
			}
		}
		if d := s.D; d > maxDur {
			maxDur = d
		}
		lines = append(lines, fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d\n", s.S, s.O))
		lines = append(lines, fmt.Sprintf("#EXTINF:%.3f,\n", s.D))
		lines = append(lines, fmt.Sprintf("/media/%s/%s/%s\n", platform, login, name))
	}
	if len(lines) == 0 {
		http.NotFound(w, r)
		return
	}
	target := int64(maxDur + 0.999)
	if target < 4 {
		target = 4
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	start := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-TARGETDURATION:" + strconv.FormatInt(target, 10) + "\n#EXT-X-MEDIA-SEQUENCE:0\n"
	end := ""
	if !growing {
		end = "#EXT-X-ENDLIST\n"
	}
	io.WriteString(w, start)
	for _, l := range lines {
		io.WriteString(w, l)
	}
	io.WriteString(w, end)
}

// handleFileList lists the recordings of one channel. The per-segment .idx
// sidecars are hidden: they only power the live growing-file playlist and
// shouldn't clutter the panel (they stay visible over FTP).
func (a *App) handleFileList(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	login := strings.ToLower(r.PathValue("login"))
	dir := a.recordingDir(platform, login)
	active := a.activeRecFile(platform, login)
	type entry struct {
		Name string `json:"name"`
		Ext  string `json:"ext"`
		Size int64  `json:"size"`
		Mod  string `json:"mod"`
		Rec  bool   `json:"rec"`
	}
	out := []entry{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasSuffix(e.Name(), ".idx") {
				continue
			}
			fi, _ := e.Info()
			if fi == nil {
				continue
			}
			out = append(out, entry{
				Name: e.Name(),
				Ext:  strings.TrimPrefix(filepath.Ext(e.Name()), "."),
				Size: fi.Size(),
				Mod:  fi.ModTime().Format("02.01.2006 15:04"),
				Rec:  e.Name() == active,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mod != out[j].Mod {
			return out[i].Mod > out[j].Mod
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, map[string]interface{}{"login": login, "dir": dir, "files": out})
}

// handleFileDelete removes a recording from the channel folder (and its .idx
// sidecar, which would otherwise stay behind as an orphan).
func (a *App) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	p, err := a.mediaPath(r.PathValue("platform"), r.PathValue("login"), r.PathValue("name"))
	if err != nil {
		writeErr(w, "file not found")
		return
	}
	if err := os.Remove(p); err != nil {
		writeErr(w, err.Error())
		return
	}
	_ = os.Remove(p + ".idx")
	writeJSON(w, map[string]bool{"ok": true})
}

// handleFileClear empties a channel folder (all recordings and sidecars).
// It refuses to run while a recording for this channel is still growing.
func (a *App) handleFileClear(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	login := strings.ToLower(r.PathValue("login"))
	dir := a.recordingDir(platform, login)
	if rec := a.activeRecFile(platform, login); rec != "" {
		writeErr(w, "trwa nagrywanie ("+rec+") — poczekaj aż się zakończy")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
		writeErr(w, err.Error())
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleFileConvert remuxes a recorded .ts into an .mp4 on demand.
func (a *App) handleFileConvert(w http.ResponseWriter, r *http.Request) {
	p, err := a.mediaPath(r.PathValue("platform"), r.PathValue("login"), r.PathValue("name"))
	if err != nil || filepath.Ext(p) != ".ts" {
		writeErr(w, "only .ts files can be converted")
		return
	}
	if _, err := a.convertMP4(p); err != nil {
		writeErr(w, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// sanitizeRename validates an incoming rename target: no path separators, no
// traversal, non-empty base. Returns "" when invalid.
func sanitizeRename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") || strings.ContainsRune(s, 0) {
		return ""
	}
	return s
}

// handleFileRename renames a recording inside its channel folder (the sidecar
// .idx follows along). The source file's extension is preserved so playback
// and the "→ MP4" conversion keep working.
func (a *App) handleFileRename(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	login := strings.ToLower(r.PathValue("login"))
	p, err := a.mediaPath(platform, login, r.PathValue("name"))
	if err != nil {
		writeErr(w, "file not found")
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "bad request")
		return
	}
	newName := sanitizeRename(body.To)
	if newName == "" {
		writeErr(w, "nieprawidłowa nazwa pliku")
		return
	}
	// keep the original extension
	ext := filepath.Ext(p)
	if ext != "" && !strings.HasSuffix(strings.ToLower(newName), strings.ToLower(ext)) {
		newName += ext
	}
	target := filepath.Join(filepath.Dir(p), newName)
	if target == p {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if active := a.activeRecFile(plats(platform), login); active != "" && active == filepath.Base(p) {
		writeErr(w, "trwa nagrywanie tego pliku")
		return
	}
	if _, err := os.Lstat(target); err == nil {
		writeErr(w, "plik "+newName+" już istnieje")
		return
	}
	if err := os.Rename(p, target); err != nil {
		writeErr(w, err.Error())
		return
	}
	_ = os.Chmod(target, 0o666)
	if _, err := os.Lstat(p + ".idx"); err == nil {
		_ = os.Rename(p+".idx", target+".idx")
		_ = os.Chmod(target+".idx", 0o666)
	}
	writeJSON(w, map[string]interface{}{"ok": true, "name": filepath.Base(target)})
}

func main() {
	port := flag.Int("port", 8090, "http port")
	tlsport := flag.Int("tlsport", 8443, "https (self-signed) port, 0 disables")
	root := flag.String("root", ".", "QPKG root (contains 'twitchdvr')")
	data := flag.String("data", "", "data dir (overrides <root>/twitchdvr/data)")
	flag.Parse()

	dataDir := *data
	if dataDir == "" {
		dataDir = filepath.Join(*root, "twitchdvr", "data")
	}
	app, err := NewApp(*root, dataDir)
	if err != nil {
		log.Fatalf("init failed: %v", err)
	}
	go app.monitor()
	mux := app.setupRoutes()
	addr := fmt.Sprintf("0.0.0.0:%d", *port)
	log.Printf("Stream DVR web listening on %s (data: %s)", addr, dataDir)
	if *tlsport > 0 {
		// Serve the same UI over HTTPS with a locally generated self-signed
		// certificate. Browsers require a secure context for notifications (and
		// several other Web APIs), which plain HTTP to a NAS IP can never be.
		// Self-signed is a free fallback for the LAN; users can switch to a
		// properly trusted certificate (QNAP HTTPS / reverse proxy) whenever.
		go func() {
			crt, key, cerr := ensureSelfSignedCert(dataDir)
			if cerr != nil {
				log.Printf("https disabled: %v", cerr)
				return
			}
			tlsAddr := fmt.Sprintf("0.0.0.0:%d", *tlsport)
			log.Printf("Stream DVR HTTPS (self-signed) listening on %s", tlsAddr)
			if err := http.ListenAndServeTLS(tlsAddr, crt, key, mux); err != nil {
				log.Printf("https listener (%s): %v", tlsAddr, err)
			}
		}()
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// ensureSelfSignedCert creates a self-signed server certificate (server.crt /
// server.key) in dir when one is missing or unreadable, and returns the file
// paths. The certificate carries SANs for localhost and every non-loopback
// interface address, so the browser's secure-context check succeeds for the
// QNAP's actual LAN IP when the UI is opened over HTTPS.
func ensureSelfSignedCert(dir string) (string, string, error) {
	crt := filepath.Join(dir, "server.crt")
	key := filepath.Join(dir, "server.key")
	if _, e1 := os.Stat(crt); e1 == nil {
		if _, e2 := os.Stat(key); e2 == nil {
			if _, err := tls.LoadX509KeyPair(crt, key); err == nil {
				return crt, key, nil
			}
		}
	}

	dns := []string{"localhost", "streamdvr"}
	if host, err := os.Hostname(); err == nil && host != "" {
		dns = append(dns, host)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			addrs, err := i.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && !ip.IsLoopback() {
					ips = append(ips, ip)
				}
			}
		}
	}

	pk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "streamdvr"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &pk.PublicKey, pk)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	certOut, err := os.OpenFile(crt, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := certOut.Close(); err != nil {
		return "", "", err
	}
	kb, err := x509.MarshalECPrivateKey(pk)
	if err != nil {
		return "", "", err
	}
	keyOut, err := os.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := keyOut.Close(); err != nil {
		return "", "", err
	}
	return crt, key, nil
}
