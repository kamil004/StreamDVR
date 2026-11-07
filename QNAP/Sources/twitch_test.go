package main

import (
	"net/http"
	"testing"
)

// --- Twitch variant picker tests ---

func TestResolveUsher(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/playlist.m3u8", "https://example.com/playlist.m3u8"},
		{"http://example.com/playlist.m3u8", "http://example.com/playlist.m3u8"},
		{"/api/channel/hls/shroud.m3u8", "https://usher.ttvnw.net/api/channel/hls/shroud.m3u8"},
		{"720p60/index.m3u8", "https://usher.ttvnw.net/api/channel/hls/720p60/index.m3u8"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveUsher(tt.input)
			if result != tt.expected {
				t.Errorf("resolveUsher(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPickVariantInfo(t *testing.T) {
	// Simple master playlist with multiple variants
	master := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
1080p60/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
720p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=854x480
480p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=640x360
360p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=100000,RESOLUTION=0x0,name="audio_only"
audio-only.m3u8
`

	uri, name, res, err := pickVariantInfo(master, resolveUsher)
	if err != nil {
		t.Fatalf("pickVariantInfo error: %v", err)
	}
	// Should pick 1080p60 (highest resolution)
	expected := "https://usher.ttvnw.net/api/channel/hls/1080p60/index.m3u8"
	if uri != expected {
		t.Errorf("pickVariantInfo uri = %q, want %q", uri, expected)
	}
	if res != "1920x1080" {
		t.Errorf("pickVariantInfo res = %q, want %q", res, "1920x1080")
	}
}

func TestPickVariantSource(t *testing.T) {
	// Source should be preferred over resolution
	master := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
1080p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=15000000,RESOLUTION=2560x1440,name="Source"
source/index.m3u8
`

	uri, _, _, err := pickVariantInfo(master, resolveUsher)
	if err != nil {
		t.Fatalf("pickVariantInfo error: %v", err)
	}
	// Should pick Source
	expected := "https://usher.ttvnw.net/api/channel/hls/source/index.m3u8"
	if uri != expected {
		t.Errorf("pickVariantInfo uri = %q, want %q", uri, expected)
	}
}

func TestPickVariantAudioOnlySkipped(t *testing.T) {
	// audio_only variants should be skipped
	master := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=100000,name="audio_only"
audio-only.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
720p/index.m3u8
`

	uri, _, _, err := pickVariantInfo(master, resolveUsher)
	if err != nil {
		t.Fatalf("pickVariantInfo error: %v", err)
	}
	expected := "https://usher.ttvnw.net/api/channel/hls/720p/index.m3u8"
	if uri != expected {
		t.Errorf("pickVariantInfo uri = %q, want %q", uri, expected)
	}
}

func TestValidateToken(t *testing.T) {
	// Create a mock HTTP server that returns a valid Twitch GraphQL response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"currentUser": {
					"id": "12345",
					"login": "testuser",
					"displayName": "Test User"
				}
			}
		}`))
	}))
	defer server.Close()

	// Override gqlURL for testing
	oldURL := gqlURL
	gqlURL = server.URL
	defer func() { gqlURL = oldURL }()

	username, err := validateToken(http.DefaultClient, "fake-token")
	if err != nil {
		t.Fatalf("validateToken error: %v", err)
	}
	if username != "Test User" {
		t.Errorf("validateToken = %q, want %q", username, "Test User")
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data": {"currentUser": null}}`))
	}))
	defer server.Close()

	oldURL := gqlURL
	gqlURL = server.URL
	defer func() { gqlURL = oldURL }()

	_, err := validateToken(http.DefaultClient, "invalid-token")
	if err == nil {
		t.Fatal("validateToken should return error for invalid token")
	}
}

func TestFetchUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"user": {
					"profileImageURL": "https://example.com/avatar.jpg"
				}
			}
		}`))
	}))
	defer server.Close()

	oldURL := gqlURL
	gqlURL = server.URL
	defer func() { gqlURL = oldURL }()

	info, err := fetchUserInfo(http.DefaultClient, "shroud")
	if err != nil {
		t.Fatalf("fetchUserInfo error: %v", err)
	}
	if info["profileImageURL"] != "https://example.com/avatar.jpg" {
		t.Errorf("fetchUserInfo avatar = %q, want %q", info["profileImageURL"], "https://example.com/avatar.jpg")
	}
}
