package main

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

// --- loginFromInput tests ---

func TestLoginFromInput(t *testing.T) {
	tests := []struct {
		input    string
		login    string
		platform string
	}{
		// Twitch URLs
		{"https://www.twitch.tv/shroud", "shroud", ""},
		{"https://twitch.tv/ninja", "ninja", ""},
		{"twitch.tv/xqc", "xqc", ""},
		// Kick URLs
		{"https://kick.com/odablock", "odablock", "kick"},
		{"https://kick.tv/someone", "someone", "kick"},
		{"kick.com/tokisachannel", "tokisachannel", "kick"},
		// Chaturbate URLs
		{"https://chaturbate.com/model123/", "model123", "chaturbate"},
		{"https://pl.chaturbate.com/sweetgirl/", "sweetgirl", "chaturbate"},
		{"chaturbate.com/room/", "room", "chaturbate"},
		// Plain names (no platform detected)
		{"shroud", "shroud", ""},
		{"Ninja", "ninja", ""},
		// With query params and fragments
		{"https://www.twitch.tv/shroud?param=value", "shroud", ""},
		{"https://kick.com/odablock#section", "odablock", "kick"},
		// Whitespace
		{"  shroud  ", "shroud", ""},
		// Edge cases
		{"", "", ""},
		{"   ", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			login, platform := loginFromInput(tt.input)
			if login != tt.login {
				t.Errorf("loginFromInput(%q) login = %q, want %q", tt.input, login, tt.login)
			}
			if platform != tt.platform {
				t.Errorf("loginFromInput(%q) platform = %q, want %q", tt.input, platform, tt.platform)
			}
		})
	}
}

// --- sanitizeRename tests ---

func TestSanitizeRename(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"my_recording", "my_recording"},
		{"New Stream 2024", "New Stream 2024"},
		{"", ""},
		{"  ", ""},
		{".", ""},
		{"..", ""},
		{"path/to/file", ""},
		{"file\\name", ""},
		{"file\x00name", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeRename(tt.input)
			if result != tt.expect {
				t.Errorf("sanitizeRename(%q) = %q, want %q", tt.input, result, tt.expect)
			}
		})
	}
}

// --- queryPlatform tests ---

func TestQueryPlatform(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"twitch", "twitch"},
		{"kick", "kick"},
		{"chaturbate", "chaturbate"},
		{"", "twitch"}, // default
		{"unknown", "twitch"}, // default
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/channels?platform="+url.QueryEscape(tt.query), nil)
			result := queryPlatform(req)
			if result != tt.expected {
				t.Errorf("queryPlatform(%q) = %q, want %q", tt.query, result, tt.expected)
			}
		})
	}
}

// --- plats tests ---

func TestPlats(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"twitch", "twitch"},
		{"Twitch", "twitch"},
		{"KICK", "kick"},
		{"chaturbate", "chaturbate"},
		{"Chaturbate", "chaturbate"},
		{"unknown", "twitch"}, // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := plats(tt.input)
			if result != tt.expected {
				t.Errorf("plats(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- recKey tests ---

func TestRecKey(t *testing.T) {
	tests := []struct {
		platform string
		login    string
		expected string
	}{
		{"twitch", "shroud", "twitch:shroud"},
		{"Twitch", "Shroud", "twitch:shroud"},
		{"kick", "odablock", "kick:odablock"},
		{"", "shroud", "twitch:shroud"}, // default platform
	}

	for _, tt := range tests {
		t.Run(tt.platform+":"+tt.login, func(t *testing.T) {
			result := recKey(tt.platform, tt.login)
			if result != tt.expected {
				t.Errorf("recKey(%q, %q) = %q, want %q", tt.platform, tt.login, result, tt.expected)
			}
		})
	}
}

// --- mediaPath path traversal protection ---

func TestMediaPathTraversal(t *testing.T) {
	a := &App{
		cfg: Config{OutDir: "/share/Public/StreamDVR"},
	}

	tests := []struct {
		platform string
		login    string
		name     string
		wantErr  bool
	}{
		{"twitch", "shroud", "recording.ts", false},
		{"twitch", "shroud", "../etc/passwd", true},
		{"twitch", "shroud", "..\\windows\\system32", true},
		{"twitch", "shroud", ".", true},
		{"twitch", "shroud", "..", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.mediaPath(tt.platform, tt.login, tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("mediaPath(%q, %q, %q) error = %v, wantErr %v", tt.platform, tt.login, tt.name, err, tt.wantErr)
			}
		})
	}
}
