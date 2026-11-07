package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Kick is recorded directly from its public HLS: the channel API gives a
// playback_url (master playlist), we pick the best variant and download the
// segments exactly like the Twitch pipeline. Login is optional and only
// personalizes the stream; anonymous playback works too.
//
// Kick authenticates through several cookies (session_token,
// kick_session_id, kick_session, ...) — there is no reliable single
// "auth-token" value anymore. The panel therefore accepts the ENTIRE Cookie
// line from the DevTools Network tab and replays it verbatim, like the macOS
// reference app does. A bare value is still treated as the legacy auth-token.

// kickCookieLine normalizes a stored/typed credential into a Cookie header
// value: a bare token becomes the legacy "auth-token=<value>", while a string
// containing "=" (a full cookie line / "k=v; k2=v2") is passed through as-is.
func kickCookieLine(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	start, end := strings.Index(v, "="), strings.Index(v, ";")
	if start == -1 || (end != -1 && end < start) {
		return "auth-token=" + v
	}
	return v
}

// kickHeaders builds browser-like request headers for the Kick API/HLS. The
// Cookie line is only attached when a credential exists.
func kickHeaders(cookie, referer string) map[string]string {
	h := map[string]string{
		"Accept":             "application/json, text/plain, */*",
		"Accept-Language":    "pl-PL,pl;q=0.9,en-US;q=0.8,en;q=0.7",
		"User-Agent":         ua,
		"sec-ch-ua":          `"Chromium";v="116", "Not)A;Brand";v="24", "Google Chrome";v="116"`,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"macOS"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-origin",
	}
	if referer != "" {
		h["Referer"] = referer
	} else {
		h["Referer"] = "https://kick.com/"
	}
	if cl := kickCookieLine(cookie); cl != "" {
		h["Cookie"] = cl
	}
	return h
}

// kickChannel fetches the public channel JSON for a login.
// A 404 means the channel does not exist.
func kickChannel(c *http.Client, cookie, login string) (map[string]interface{}, error) {
	return kickChannelAt(c, cookie, login, "https://kick.com")
}

// kickChannelAt is kickChannel against a given base URL (used by tests).
func kickChannelAt(c *http.Client, cookie, login, base string) (map[string]interface{}, error) {
	return kickChannelAtURL(c, cookie, base+"/api/v2/channels/"+url.PathEscape(strings.ToLower(login)))
}

// kickChannelAtURL fetches and parses a raw channel JSON endpoint.
func kickChannelAtURL(c *http.Client, cookie, u string) (map[string]interface{}, error) {
	b, err := reqCtx(c, context.Background(), "GET", u, nil, kickHeaders(cookie, ""))
	if err != nil {
		return nil, err
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// kickValidate checks a kick credential (full cookie line or legacy auth-token
// value) and returns the signed-in username.
func kickValidate(c *http.Client, cred string) (string, error) {
	return kickValidateAt(c, cred, "https://kick.com")
}

// kickValidateAt is kickValidate against a given base URL (used by tests).
// Kick's /api/v2/users/me returns 404 (not 401) when the session cookies are
// missing, expired, or bogus, so the plain HTTP status is mapped to a Polish
// hint telling the user exactly which header line to copy.
func kickValidateAt(c *http.Client, cred, base string) (string, error) {
	if strings.TrimSpace(cred) == "" {
		return "", fmt.Errorf("podaj ciasteczka logowania do kick.com — nagrywanie działa też bez logowania, ale jeśli chcesz się zalogować, skopiuj CAŁĄ linię ciasteczek: F12 → Network → kliknij dowolny request do kick.com → Request Headers → wklej CAŁĄ wartość nagłówka cookie")
	}
	u := base + "/api/v2/users/me"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	for k, v := range kickHeaders(cred, "") {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		switch resp.StatusCode {
		case 404:
			return "", fmt.Errorf("kick.com nie rozpoznaje tych ciasteczek (HTTP 404) — sesja jest nieprawidłowa lub wygasła. Zaloguj się ponownie na kick.com w tym oknie i wklej CAŁĄ linię nagłówka cookie (F12 → Network → request do kick.com → Request Headers → cookie:)")
		case 401, 403:
			return "", fmt.Errorf("kick.com odrzuca zapytanie (HTTP %d) — możliwe, że wymaga ponownej weryfikacji Cloudflare. Otwórz kick.com w nowym oknie, zaloguj się, wróć i wklej świeżą całą linię nagłówka cookie", resp.StatusCode)
		default:
			return "", fmt.Errorf("kick.com odpowiedziało %s — spróbuj ponownie za chwilę", resp.Status)
		}
	}
	var obj struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return "", fmt.Errorf("kick: bad response")
	}
	if obj.Username == "" {
		return "", fmt.Errorf("kick nie zwrócił nazwy użytkownika — wklej CAŁĄ linię nagłówka cookie (F12 → Network → request do kick.com → Request Headers → cookie:)")
	}
	return obj.Username, nil
}

// kickLive returns (live, title) for a channel. The `livestream` object is
// authoritative; when the API omits it (unauthenticated requests sometimes do),
// the playback_url is probed like the macOS app does.
func kickLive(c *http.Client, cookie, login string) (bool, string, error) {
	obj, err := kickChannel(c, cookie, login)
	if err != nil {
		return false, "", err
	}
	if ls, ok := obj["livestream"].(map[string]interface{}); ok && ls != nil {
		title, _ := ls["session_title"].(string)
		return true, title, nil
	}
	pu, _ := obj["playback_url"].(string)
	if pu == "" {
		return false, "", nil
	}
	if !strings.HasPrefix(pu, "http") {
		return false, "", nil
	}
	return probeKickMaster(c, cookie, pu)
}

// probeKickMaster confirms a stream is up by fetching the HLS master playlist
// and looking for variant/segment markers; offline/expired URLs return 404.
func probeKickMaster(c *http.Client, cookie, u string) (bool, string, error) {
	b, err := reqCtx(c, context.Background(), "GET", u, nil, kickHeaders(cookie, ""))
	if err != nil {
		return false, "", nil
	}
	body := string(b)
	live := strings.Contains(body, "EXT-X-STREAM-INF") ||
		strings.Contains(body, "EXT-X-MEDIA") ||
		strings.Contains(body, "EXTINF")
	if !live {
		return false, "", nil
	}
	// title is not reliably available from the master; fall back to the channel
	return true, "", nil
}

// kickMaster fetches the master playlist and returns its text plus the base
// URL used to resolve relative variant/segment URIs.
func kickMaster(c *http.Client, cookie, login string) (string, string, error) {
	obj, err := kickChannel(c, cookie, login)
	if err != nil {
		return "", "", err
	}
	pu, _ := obj["playback_url"].(string)
	if pu == "" {
		return "", "", fmt.Errorf("kick: no playback url for %s (offline?)", login)
	}
	headers := kickHeaders(cookie, "https://kick.com/"+url.PathEscape(strings.ToLower(login)))
	b, err := reqCtx(c, context.Background(), "GET", pu, nil, headers)
	if err != nil {
		return "", "", err
	}
	return string(b), pu, nil
}

// kickAvatar reads the channel's profile picture from the public API.
func kickAvatar(c *http.Client, cookie, login string) string {
	obj, err := kickChannel(c, cookie, login)
	if err != nil {
		return ""
	}
	user, _ := obj["user"].(map[string]interface{})
	pic, _ := user["profile_pic"].(string)
	return pic
}

// resolveKick resolves a relative playlist/segment URI against the master base.
func resolveKick(uri, base string) string {
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	bu, err := url.Parse(base)
	if err != nil {
		return uri
	}
	ru, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return bu.ResolveReference(ru).String()
}
