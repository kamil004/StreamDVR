package main

import (
	"net/http"
	"testing"
)

// --- Kick cookie line tests ---

func TestKickCookieLine(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Full cookie line (contains = before ;)
		{"k_session=abc123; auth-token=xyz", "k_session=abc123; auth-token=xyz"},
		// Bare token (no = or ; before first ;)
		{"abc123", "auth-token=abc123"},
		// Empty
		{"", ""},
		// Token with = in value
		{"auth-token=abc; session=xyz", "auth-token=abc; session=xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := kickCookieLine(tt.input)
			if result != tt.expected {
				t.Errorf("kickCookieLine(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- Kick resolve tests ---

func TestResolveKick(t *testing.T) {
	tests := []struct {
		uri      string
		base     string
		expected string
	}{
		{"https://example.com/playlist.m3u8", "https://kick.com/", "https://example.com/playlist.m3u8"},
		{"720p/index.m3u8", "https://kick.com/api/playback/abc", "https://kick.com/api/playback/720p/index.m3u8"},
		{"/hls/playlist.m3u8", "https://kick.com/api/playback/abc", "https://kick.com/hls/playlist.m3u8"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			result := resolveKick(tt.uri, tt.base)
			if result != tt.expected {
				t.Errorf("resolveKick(%q, %q) = %q, want %q", tt.uri, tt.base, result, tt.expected)
			}
		})
	}
}

// --- Kick validate tests ---

func TestKickValidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"username": "testuser"}`))
	}))
	defer server.Close()

	// Override kickChannelAtURL for testing
	username, err := kickValidateAt(http.DefaultClient, "k_session=abc", server.URL)
	if err != nil {
		t.Fatalf("kickValidateAt error: %v", err)
	}
	if username != "testuser" {
		t.Errorf("kickValidateAt = %q, want %q", username, "testuser")
	}
}

func TestKickValidateEmpty(t *testing.T) {
	_, err := kickValidate(http.DefaultClient, "")
	if err == nil {
		t.Fatal("kickValidate should return error for empty credential")
	}
}
