package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func reqCtx(c *http.Client, ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", ua)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := c.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(b), 120))
	}
	return b, nil
}

func req(c *http.Client, method, url string, body []byte, headers map[string]string) ([]byte, error) {
	return reqCtx(c, context.Background(), method, url, body, headers)
}

func gql(c *http.Client, token, query string, vars map[string]interface{}) (map[string]interface{}, error) {
	return gqlCtx(c, context.Background(), token, query, vars)
}

func gqlCtx(c *http.Client, ctx context.Context, token, query string, vars map[string]interface{}) (map[string]interface{}, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"query":     query,
		"variables": vars,
	})
	headers := map[string]string{"Client-ID": clientID, "Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "OAuth " + token
	}
	b, err := reqCtx(c, ctx, "POST", gqlURL, payload, headers)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
		Err  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	if len(resp.Err) > 0 {
		return nil, fmt.Errorf("twitch: %s", resp.Err[0].Message)
	}
	return resp.Data, nil
}

// validateToken checks the token and returns the Twitch username.
// Twitch web "auth-token" cookies are NOT Helix OAuth tokens, so the Helix
// /users endpoint rejects them with HTTP 404. The public GraphQL currentUser
// query accepts those cookies (Authorization: OAuth <token>) and returns the
// signed-in account, which is what the Twitch web player itself uses.
func validateToken(c *http.Client, token string) (string, error) {
	q := `query { currentUser { id login displayName } }`
	data, err := gql(c, token, q, nil)
	if err != nil {
		return "", err
	}
	cu, _ := data["currentUser"].(map[string]interface{})
	if cu == nil {
		return "", fmt.Errorf("token is not valid - zaloguj się na twitch.tv (F12 → Application → Cookies → twitch.tv → auth-token) i skopiuj całą wartość")
	}
	name, _ := cu["displayName"].(string)
	if name == "" {
		name, _ = cu["login"].(string)
	}
	if name == "" {
		return "", fmt.Errorf("could not resolve account name")
	}
	return name, nil
}

// fetchUserInfo returns public profile info (avatar) for a channel. Public
// data needs no auth.
func fetchUserInfo(c *http.Client, login string) (map[string]string, error) {
	q := `query($login: String!){ user(login: $login){ profileImageURL(width: 300) } }`
	data, err := gql(c, "", q, map[string]interface{}{"login": login})
	if err != nil {
		return nil, err
	}
	user, _ := data["user"].(map[string]interface{})
	if user == nil {
		return nil, fmt.Errorf("unknown channel")
	}
	info := map[string]string{}
	info["profileImageURL"], _ = user["profileImageURL"].(string)
	return info, nil
}

// fetchLive returns (live, title) for a channel using the public GraphQL.
func fetchLive(c *http.Client, token, login string) (bool, string, error) {
	return fetchLiveCtx(c, context.Background(), token, login)
}

// fetchLiveCtx is fetchLive with a cancellable context, used by recorders so a
// manual Stop interrupts the in-flight status check immediately.
func fetchLiveCtx(c *http.Client, ctx context.Context, token, login string) (bool, string, error) {
	q := `query($login: String!){ user(login: $login){ stream { type title } } }`
	data, err := gqlCtx(c, ctx, token, q, map[string]interface{}{"login": login})
	if err != nil {
		return false, "", err
	}
	user, _ := data["user"].(map[string]interface{})
	stream, _ := user["stream"].(map[string]interface{})
	if stream == nil {
		return false, "", nil
	}
	typ, _ := stream["type"].(string)
	if typ != "live" {
		return false, "", nil
	}
	title, _ := stream["title"].(string)
	return true, title, nil
}

// playback returns the PlaybackAccessToken (sig + value) for a channel.
func playback(c *http.Client, token, login string) (string, string, error) {
	return playbackCtx(c, context.Background(), token, login)
}

func playbackCtx(c *http.Client, ctx context.Context, token, login string) (string, string, error) {
	q := `query($login: String!){
	  streamPlaybackAccessToken(channelName: $login,
	    params: {platform: "web", playerBackend: "mediaplayer", playerType: "site"}) {
	    value signature
	  }}`
	data, err := gqlCtx(c, ctx, token, q, map[string]interface{}{"login": login})
	if err != nil {
		return "", "", err
	}
	pt, _ := data["streamPlaybackAccessToken"].(map[string]interface{})
	sig, _ := pt["signature"].(string)
	val, _ := pt["value"].(string)
	if sig == "" || val == "" {
		return "", "", fmt.Errorf("empty playback token for %s (offline?)", login)
	}
	return sig, val, nil
}

// masterPlaylist fetches the usher master playlist for a channel using the
// PlaybackAccessToken (sig + token) so it always resolves to a concrete
// playable HLS master (m3u8) rather than a site URL.
func masterPlaylist(c *http.Client, token, login, sig, ptoken string) (string, error) {
	return masterPlaylistCtx(c, context.Background(), token, login, sig, ptoken)
}

func masterPlaylistCtx(c *http.Client, ctx context.Context, token, login, sig, ptoken string) (string, error) {
	u := "https://usher.ttvnw.net/api/channel/hls/" + url.PathEscape(login) + ".m3u8"
	params := url.Values{}
	params.Set("player", "twitchweb")
	params.Set("allow_audio_only", "true")
	params.Set("allow_source", "true")
	params.Set("type", "any")
	params.Set("fast_bread", "true")
	params.Set("p", fmt.Sprint(time.Now().UnixMilli()))
	params.Set("sig", sig)
	params.Set("token", ptoken)
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "OAuth " + token
	}
	b, err := reqCtx(c, ctx, "GET", u+"?"+params.Encode(), nil, headers)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// resolveUsher turns a relative/absolute playlist URI from a usher master
// playlist into an absolute URL. Usher serves relative paths like
// "720p60/index.m3u8" which point under the channel hls root.
func resolveUsher(uri string) string {
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	if strings.HasPrefix(uri, "/") {
		return "https://usher.ttvnw.net" + uri
	}
	return "https://usher.ttvnw.net/api/channel/hls/" + uri
}

// pickVariant returns the absolute URL of the best (highest-quality) media
// playlist from a usher master playlist. Prefers source, ignores audio_only
// streams, then falls back to RESOLUTION and finally BANDWIDTH.
func pickVariant(master string) (string, string, error) {
	uri, name, _, err := pickVariantInfo(master, resolveUsher)
	return uri, name, err
}

// pickVariantWith is pickVariant with a custom URI resolver (also used by the
// Kick/Chaturbate pipelines, whose masters are resolved against their own base).
func pickVariantWith(master string, resolve func(string) string) (string, string, error) {
	uri, name, _, err := pickVariantInfo(master, resolve)
	return uri, name, err
}

// pickVariantInfo is pickVariant but also returns the RESOLUTION (e.g.
// "1920x1080") of the chosen variant, shown in the recording logs. The
// resolution string is empty when the master does not advertise it.
func pickVariantInfo(master string, resolve func(string) string) (string, string, string, error) {
	var (
		bestRes  string // chosen uri
		bestName string // label of the chosen variant
		bestResS string // "WxH" of the chosen variant
		score    int64  // resolution pixels or -bandwidth
		haveSrc  bool
	)

	lines := strings.Split(master, "\n")
	name := ""
	for i, line := range lines {
		line = strings.TrimSpace(line)
		var uri string
		var bandwidth int64
		var resW, resH int64
		isSrc := false

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attrs := strings.Split(line[len("#EXT-X-STREAM-INF:"):], ",")
			for _, a := range attrs {
				a = strings.TrimSpace(a)
				switch {
				case strings.HasPrefix(a, "RESOLUTION="):
					fmt.Sscanf(a[len("RESOLUTION="):], "%dx%d", &resW, &resH)
				case strings.HasPrefix(a, "BANDWIDTH="):
					fmt.Sscanf(a[len("BANDWIDTH="):], "%d", &bandwidth)
				case strings.HasPrefix(a, "NAME="):
					if len(a) > len("NAME=")+2 {
						name = strings.Trim(a[len("NAME="):], `"`)
					}
				}
			}
			if i+1 < len(lines) {
				uri = strings.TrimSpace(lines[i+1])
			}
			if uri == "" {
				continue
			}
			if strings.Contains(strings.ToLower(uri), "audio_only") {
				continue
			}
			label := strings.ToLower(name)
			if strings.Contains(label, "source") {
				isSrc = true
			}
			switch {
			case isSrc && !haveSrc, // first source wins
				!isSrc && !haveSrc && resW*resH > score,
				!isSrc && !haveSrc && resW*resH == 0 && bandwidth > 0 && bandwidth > score:
				bestRes, bestName, score = uri, name, resW*resH
				if resW > 0 && resH > 0 {
					bestResS = fmt.Sprintf("%dx%d", resW, resH)
				}
				if isSrc {
					haveSrc = true
				}
			}
		}
	}
	if bestRes == "" {
		return "", "", "", fmt.Errorf("no usable variants in master playlist")
	}
	return resolve(bestRes), bestName, bestResS, nil
}
