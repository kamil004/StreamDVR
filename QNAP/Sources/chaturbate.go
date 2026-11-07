package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Chaturbate is recorded directly from its public HLS, like the macOS/Windows
// reference app: the room page HTML is fetched with a plain browser User-Agent
// (no login/cookies are required) and the embedded HLS master m3u8 URL is
// extracted (unescaping the \uXXXX escapes Chaturbate puts in the page). The
// recording itself is written by the bundled ffmpeg: Chaturbate serves
// LL-HLS/fMP4 fragments that concatenating into a .ts would corrupt, so ffmpeg
// demuxes them into a real MPEG-TS (see Recorder.runChaturbate). A room with
// no m3u8 in its HTML is offline; a 404 page means the room does not exist.

var (
	cbM3U8Re  = regexp.MustCompile(`https?:\\?/\\?/[^"'\s]+\.m3u8[^"'\s]*`)
	cbOGDesc  = regexp.MustCompile(`(?i)<meta\s+property="og:description"\s+content="([^"]+)"`)
	cbOGImage = regexp.MustCompile(`(?i)<meta\s+property="og:image"\s+content="([^"]+)"`)
	cbTopic   = regexp.MustCompile(`id="room-topic"[^>]*>(.*?)</div>`)
)

const chaturbateUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36"

// chaturbateRoom fetches the room page HTML for a login and returns it along
// with the raw HTTP status. A 404 means the room does not exist.
func chaturbateRoom(c *http.Client, login string) (string, int, error) {
	u := "https://chaturbate.com/" + url.PathEscape(strings.ToLower(login)) + "/"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", chaturbateUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pl-PL,pl;q=0.9,en-US;q=0.8,en;q=0.7")
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), resp.StatusCode, nil
}

// chaturbateLive reports (live, title) for a room: live iff the page embeds a
// playable HLS m3u8.

// cbRateLimit tracks per-channel request counts to avoid hammering Chaturbate.
// After 10 failed requests within 5 minutes, the channel is skipped for 10 minutes.
type cbRateLimit struct {
	fails     int
	lastFail  time.Time
	blocked   time.Time
}

var (
	cbRateMu     sync.Mutex
	cbRateLimits = make(map[string]*cbRateLimit)
	cbRateMaxFails = 10
	cbRateWindow       = 5 * time.Minute
	cbRateBlock        = 10 * time.Minute
)

func cbCheckRateLimit(login string) bool {
	cbRateMu.Lock()
	defer cbRateMu.Unlock()
	rl := cbRateLimits[login]
	if rl == nil {
		rl = &cbRateLimit{}
		cbRateLimits[login] = rl
	}
	now := time.Now()
	// If currently blocked, check if block expired
	if !rl.blocked.IsZero() && now.Before(rl.blocked) {
		return false // still blocked
	}
	// Reset counter if window expired
	if !rl.lastFail.IsZero() && now.Sub(rl.lastFail) > cbRateWindow {
		rl.fails = 0
	}
	return true
}

func cbRecordFail(login string) {
	cbRateMu.Lock()
	defer cbRateMu.Unlock()
	rl := cbRateLimits[login]
	if rl == nil {
		rl = &cbRateLimit{}
		cbRateLimits[login] = rl
	}
	rl.fails++
	rl.lastFail = time.Now()
	if rl.fails >= cbRateMaxFails {
		rl.blocked = time.Now().Add(cbRateBlock)
	}
}

func cbRecordSuccess(login string) {
	cbRateMu.Lock()
	defer cbRateMu.Unlock()
	rl := cbRateLimits[login]
	if rl == nil {
		rl = &cbRateLimit{}
		cbRateLimits[login] = rl
	}
	rl.fails = 0
	rl.lastFail = time.Time{}
	rl.blocked = time.Time{}
}

func chaturbateLive(c *http.Client, login string) (bool, string, error) {
	html, status, err := chaturbateRoom(c, login)
	if err != nil {
		return false, "", err
	}
	if status == 404 {
		return false, "", fmt.Errorf("chaturbate: pokój %s nie istnieje", login)
	}
	if status != 200 {
		return false, "", fmt.Errorf("chaturbate odpowiedziało HTTP %d (tryb offline?)", status)
	}
	if cbM3U8(html) == "" {
		return false, "", nil
	}
	return true, cbTitle(html), nil
}

// chaturbateMaster fetches the master playlist for a live room and returns its
// text plus the base URL used to resolve relative variant/segment URIs.
func chaturbateMaster(c *http.Client, login string) (string, string, error) {
	html, status, err := chaturbateRoom(c, login)
	if err != nil {
		return "", "", err
	}
	if status == 404 {
		return "", "", fmt.Errorf("chaturbate: pokój %s nie istnieje", login)
	}
	if status != 200 {
		return "", "", fmt.Errorf("chaturbate odpowiedziało HTTP %d", status)
	}
	raw := cbM3U8(html)
	if raw == "" {
		return "", "", fmt.Errorf("chaturbate: %s jest offline (brak m3u8 w stronie)", login)
	}
	ref := "https://chaturbate.com/" + url.PathEscape(strings.ToLower(login)) + "/"
	headers := map[string]string{
		"User-Agent": chaturbateUA,
		"Accept":     "*/*",
		"Referer":    ref,
	}
	body, err := reqCtx(c, context.Background(), "GET", raw, nil, headers)
	if err != nil {
		return "", "", err
	}
	return string(body), raw, nil
}

// chaturbateAvatar reads the room's profile picture (og:image).
func chaturbateAvatar(c *http.Client, login string) string {
	html, status, err := chaturbateRoom(c, login)
	if err != nil || status != 200 {
		return ""
	}
	return cbAvatar(html)
}

// cbM3U8 extracts the HLS master URL from a room page and unescapes the
// \uXXXX / \/ / &amp; escapes Chaturbate embeds. Empty when the room is offline.
func cbM3U8(html string) string {
	m := cbM3U8Re.FindString(html)
	if m == "" {
		return ""
	}
	for _, sep := range []string{`\u0022`, `"`, `,`} {
		if i := strings.Index(m, sep); i >= 0 {
			m = m[:i]
		}
	}
	m = strings.ReplaceAll(m, `\/`, `/`)
	m = cbDecodeUnicode(m)
	m = strings.ReplaceAll(m, "&amp;", "&")
	return m
}

func cbDecodeUnicode(s string) string {
	re := regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		hex := m[2:]
		if n, err := strconv.ParseUint(hex, 16, 32); err == nil {
			return string(rune(n))
		}
		return m
	})
}

// cbTitle returns the room's stream title (og:description or the room-topic
// div), HTML-entity-decoded.
func cbTitle(html string) string {
	if m := cbOGDesc.FindStringSubmatch(html); len(m) > 1 {
		if t := strings.TrimSpace(cbEntities(m[1])); t != "" {
			return t
		}
	}
	if m := cbTopic.FindStringSubmatch(html); len(m) > 1 {
		if t := strings.TrimSpace(cbEntities(m[1])); t != "" {
			return t
		}
	}
	return "Live stream"
}

// cbAvatar returns the room's og:image.
func cbAvatar(html string) string {
	if m := cbOGImage.FindStringSubmatch(html); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func cbEntities(s string) string {
	for _, p := range [][2]string{
		{"&amp;", "&"}, {"&quot;", `"`}, {"&#39;", "'"},
		{"&lt;", "<"}, {"&gt;", ">"}, {"&nbsp;", " "}, {"&#x27;", "'"},
	} {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	return s
}

// cbRendition is one playable chaturbate rendition: the resolved HLS media
// playlist URLs. Chaturbate serves LL-HLS where every video variant references
// a separate audio playlist (#EXT-X-MEDIA audio rendition); feeding ffmpeg
// only the video playlist silently drops all audio from the recording.
type cbRendition struct {
	video string // highest-bandwidth video media playlist
	audio string // its AUDIO-group media playlist; "" when audio is muxed in
	res   string // "WxH" of the chosen video rendition
}

// cbBestStreams picks the highest-bandwidth video rendition in a chaturbate
// master playlist and returns its video playlist together with the audio
// playlist of its AUDIO group. Relative URIs are resolved against base, like
// the browser does. An error is returned when the master advertises no video
// rendition at all.
func cbBestStreams(master, base string) (cbRendition, error) {
	baseU, _ := url.Parse(base)
	resolve := func(u string) string {
		ref, err := url.Parse(strings.TrimSpace(u))
		if err != nil {
			ref = &url.URL{Path: strings.TrimSpace(u)}
		}
		if ref.IsAbs() {
			return ref.String()
		}
		if baseU == nil {
			return ref.String()
		}
		return baseU.ResolveReference(ref).String()
	}

	// pass 1: the audio renditions, keyed by GROUP-ID. These lines come before
	// the variants in real chaturbate masters, but scanning twice is robust to
	// any ordering.
	audioByGroup := map[string]string{}
	for _, line := range strings.Split(master, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") || !strings.Contains(line, "TYPE=AUDIO") {
			continue
		}
		gid, uri := "", ""
		for _, a := range strings.Split(strings.TrimPrefix(line, "#EXT-X-MEDIA:"), ",") {
			a = strings.TrimSpace(a)
			switch {
			case strings.HasPrefix(a, `GROUP-ID="`):
				gid = strings.Trim(a[len(`GROUP-ID="`):], `"`)
			case strings.HasPrefix(a, `URI="`):
				uri = strings.Trim(a[len(`URI="`):], `"`)
			}
		}
		if gid != "" && uri != "" {
			audioByGroup[gid] = resolve(uri)
		}
	}

	// pass 2: the highest-bandwidth video rendition (audio-only variants like
	// "audio_only" are skipped).
	lines := strings.Split(master, "\n")
	var best cbRendition
	bestBW := -1
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		bw := 0
		res := ""
		gid := ""
		for _, a := range strings.Split(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), ",") {
			a = strings.TrimSpace(a)
			switch {
			case strings.HasPrefix(a, "BANDWIDTH="):
				bw, _ = strconv.Atoi(strings.TrimSpace(a[len("BANDWIDTH="):]))
			case strings.HasPrefix(a, "RESOLUTION="):
				res = a[len("RESOLUTION="):]
			case strings.HasPrefix(a, "AUDIO="):
				gid = strings.Trim(strings.TrimPrefix(a, "AUDIO="), `"`)
			}
		}
		if i+1 >= len(lines) {
			continue
		}
		uri := strings.TrimSpace(lines[i+1])
		if uri == "" || strings.Contains(strings.ToLower(uri), "audio") {
			continue
		}
		if bw < bestBW {
			continue
		}
		bestBW = bw
		best = cbRendition{video: resolve(uri), audio: audioByGroup[gid], res: res}
	}
	if best.video == "" {
		return cbRendition{}, fmt.Errorf("chaturbate: master has no video rendition")
	}
	return best, nil
}
