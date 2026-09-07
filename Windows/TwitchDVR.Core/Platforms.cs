using System.Text.Json;
using System.Text.RegularExpressions;

namespace TwitchDVR.Core;

/// <summary>
/// Platform-aware dispatching for Twitch / Chaturbate / Kick (mirrors the macOS edition).
/// Twitch uses the existing GraphQL TwitchApi; Chaturbate reads the room HTML to pull the
/// HLS playlist (streamlink dropped its chaturbate plugin); Kick uses the public REST API.
/// </summary>
public static class PlatformProvider
{
    private const string ChromeUserAgent =
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36";

    // Dedicated client with cookies disabled so only the explicit Cookie header is sent.
    private static readonly HttpClient Http = CreateNoCookieClient();

    private static HttpClient CreateNoCookieClient()
    {
        var handler = new HttpClientHandler { UseCookies = false };
        return new HttpClient(handler) { Timeout = TimeSpan.FromSeconds(30) };
    }

    public static string WatchUrl(StreamPlatform platform, string login) => platform switch
    {
        StreamPlatform.Chaturbate => $"https://chaturbate.com/{login}",
        StreamPlatform.Kick => $"https://kick.com/{login}",
        _ => $"https://www.twitch.tv/{login}"
    };

    /// <summary><outputDirectory>/&lt;platform&gt;/&lt;login&gt;</summary>
    public static string RecordingDirectory(string outputDirectory, StreamChannel channel)
        => Path.Combine(outputDirectory, channel.Platform.Slug(), channel.Login);

    /// <summary>
    /// Detects the platform from raw input and extracts the username. Accepts plain names
    /// (Twitch default) or URLs like https://www.twitch.tv/shroud,
    /// https://pl.chaturbate.com/sweetsweet__baby/, https://kick.com/odablock.
    /// </summary>
    public static (StreamPlatform Platform, string Login)? ParseInput(string raw)
    {
        if (string.IsNullOrWhiteSpace(raw)) return null;
        var lower = raw.ToLowerInvariant();
        var platform = StreamPlatform.Twitch;
        if (lower.Contains("chaturbate")) platform = StreamPlatform.Chaturbate;
        else if (lower.Contains("kick.com") || lower.Contains("kick.tv")) platform = StreamPlatform.Kick;

        var s = raw.Trim();
        var scheme = s.IndexOf("://", StringComparison.Ordinal);
        if (scheme >= 0) s = s[(scheme + 3)..];

        // Normalize Chaturbate locale subdomains (e.g. pl.chaturbate.com) to chaturbate.com.
        if (platform == StreamPlatform.Chaturbate)
        {
            var host = s.IndexOf("chaturbate.com", StringComparison.OrdinalIgnoreCase);
            if (host >= 0) s = s[(host + "chaturbate.com".Length)..];
        }

        // Username = last non-empty path segment.
        s = s.Trim().Trim('/');
        var slash = s.LastIndexOf('/');
        if (slash >= 0) s = s[(slash + 1)..];
        var query = s.IndexOf('?');
        if (query >= 0) s = s[..query];
        var fragment = s.IndexOf('#');
        if (fragment >= 0) s = s[..fragment];

        var login = s.Trim().ToLowerInvariant().Trim('/');
        return string.IsNullOrEmpty(login) ? null : (platform, login);
    }

    public static async Task<StreamChannel?> ResolveChannelAsync(StreamPlatform platform, string login)
    {
        switch (platform)
        {
            case StreamPlatform.Twitch:
                return await TwitchApi.GetUser(login);
            case StreamPlatform.Chaturbate:
            {
                var html = await FetchChaturbateHtmlAsync(login);
                if (html.Length == 0) return null;
                return new StreamChannel
                {
                    Id = $"chaturbate:{login}",
                    Login = login,
                    DisplayName = ChaturbateParse.DisplayName(html) ?? login,
                    Platform = StreamPlatform.Chaturbate,
                    ProfileImageUrl = ChaturbateParse.Avatar(html) ?? ""
                };
            }
            default:
            {
                var json = await FetchKickChannelAsync(login);
                if (json == null) return null;
                return new StreamChannel
                {
                    Id = $"kick:{login}",
                    Login = login,
                    DisplayName = KickParse.DisplayName(json) ?? login,
                    Platform = StreamPlatform.Kick,
                    ProfileImageUrl = KickParse.ProfilePic(json)
                };
            }
        }
    }

    /// <summary>Channel status ALWAYS returned (even when offline), including the avatar.
    /// Null only when the channel doesn't exist. Network errors throw (caller decides).</summary>
    public static async Task<ChannelStatus?> GetStatusAsync(StreamPlatform platform, string login)
    {
        switch (platform)
        {
            case StreamPlatform.Twitch:
            {
                var status = await TwitchApi.GetChannelStatus(login);
                return status;
            }
            case StreamPlatform.Chaturbate:
            {
                var html = await FetchChaturbateHtmlAsync(login);
                if (html.Length == 0) return null;
                var live = ChaturbateParse.ExtractM3U8(html) != null;
                return new ChannelStatus
                {
                    Id = $"chaturbate:{login}",
                    Login = login,
                    DisplayName = ChaturbateParse.DisplayName(html) ?? login,
                    ProfileImageURL = ChaturbateParse.Avatar(html) ?? "",
                    IsLive = live,
                    Title = live ? ChaturbateParse.Title(html) : "",
                    Game = ""
                };
            }
            default:
            {
                var json = await FetchKickChannelAsync(login);
                if (json == null) return null;
                var live = json.TryGetValue("livestream", out var l) && l.ValueKind == JsonValueKind.Object;
                return new ChannelStatus
                {
                    Id = $"kick:{login}",
                    Login = login,
                    DisplayName = KickParse.DisplayName(json) ?? login,
                    ProfileImageURL = KickParse.ProfilePic(json),
                    IsLive = live,
                    Title = live && l.TryGetProperty("session_title", out var t) ? t.GetString() ?? "" : "",
                    Game = live ? KickParse.CategoryName(l) : ""
                };
            }
        }
    }

    /// <summary>Live-stream info used to name the recording, or null when offline.</summary>
    public static async Task<StreamInfo?> GetStreamInfoAsync(StreamPlatform platform, string login)
    {
        switch (platform)
        {
            case StreamPlatform.Twitch:
            {
                var info = await TwitchApi.GetStream(login);
                if (info == null) return null;
                info.AccessToken = TwitchApi.AccessToken;
                return info;
            }
            case StreamPlatform.Chaturbate:
            {
                var html = await FetchChaturbateHtmlAsync(login);
                var m3u8 = ChaturbateParse.ExtractM3U8(html);
                if (html.Length == 0 || m3u8 == null) return null;
                return new StreamInfo
                {
                    Title = ChaturbateParse.Title(html),
                    Game = "",
                    StreamM3U8 = m3u8,
                    ProfileImageUrl = ChaturbateParse.Avatar(html) ?? ""
                };
            }
            default:
            {
                var json = await FetchKickChannelAsync(login);
                if (json == null || !json.TryGetValue("livestream", out var live) ||
                    live.ValueKind != JsonValueKind.Object) return null;
                return new StreamInfo
                {
                    Title = live.TryGetProperty("session_title", out var title) ? title.GetString() ?? "Live stream" : "Live stream",
                    Game = KickParse.CategoryName(live),
                    ViewerCount = live.TryGetProperty("viewer_count", out var vc) && vc.ValueKind == JsonValueKind.Number ? vc.GetInt32() : 0,
                    StreamM3U8 = "",
                    ProfileImageUrl = KickParse.ProfilePic(json)
                };
            }
        }
    }

    // ---- Chaturbate ----

    private static async Task<string> FetchChaturbateHtmlAsync(string login)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, $"https://chaturbate.com/{login}/");
        request.Headers.TryAddWithoutValidation("User-Agent", ChromeUserAgent);
        try
        {
            var response = await Http.SendAsync(request);
            if (response.StatusCode != System.Net.HttpStatusCode.OK) return "";
            return await response.Content.ReadAsStringAsync();
        }
        catch
        {
            return "";
        }
    }

    internal static class ChaturbateParse
    {
        /// <summary>Extracts the live room's HLS playlist URL and unescapes the embedded
        /// \uXXXX HTML/JS escapes. Null when offline.</summary>
        public static string? ExtractM3U8(string html)
        {
            if (!Captured(@"https?://[^""'\s]+\.m3u8[^""'\s]*", html, out var raw)) return null;
            var url = raw;
            foreach (var sep in new[] { "\\u0022", "\"", "," })
            {
                var idx = url.IndexOf(sep, StringComparison.Ordinal);
                if (idx >= 0) url = url[..idx];
            }
            return DecodeUnicodeEscapes(url);
        }

        public static string DecodeUnicodeEscapes(string text)
        {
            return Regex.Replace(text, @"\\u([0-9a-fA-F]{4})", m =>
            {
                var hex = m.Groups[1].Value;
                return int.TryParse(hex, System.Globalization.NumberStyles.HexNumber,
                    System.Globalization.CultureInfo.InvariantCulture, out var cp)
                    ? ((char)cp).ToString()
                    : m.Value;
            });
        }

        public static string Title(string html)
        {
            if (Captured(@"<meta\s+property=""og:description""\s+content=""([^""]+)""", html, out var desc))
            {
                var cleaned = DecodeEntities(desc.Trim());
                if (!string.IsNullOrEmpty(cleaned)) return cleaned;
            }
            if (Captured(@"id=""room-topic""[^>]*>(.*?)</div>", html, out var topic, RegexOptions.Singleline))
            {
                var cleaned = Regex.Replace(topic, "<.*?>", string.Empty).Trim();
                if (!string.IsNullOrEmpty(cleaned)) return cleaned;
            }
            return "Live stream";
        }

        public static string? Avatar(string html)
        {
            return Captured(@"<meta\s+property=""og:image""\s+content=""([^""]+)""", html, out var img) ? img : null;
        }

        public static string? DisplayName(string html)
        {
            if (Captured(@"<meta\s+property=""og:title""\s+content=""([^""]+)""", html, out var og) &&
                og.Contains("on Chaturbate", StringComparison.OrdinalIgnoreCase))
            {
                var name = og
                    .Replace(" - Chaturbate", "", StringComparison.OrdinalIgnoreCase)
                    .Replace(" on Chaturbate!", "", StringComparison.OrdinalIgnoreCase)
                    .Replace(" on Chaturbate", "", StringComparison.OrdinalIgnoreCase)
                    .Trim();
                foreach (var prefix in new[] { "Watch live ", "Watch " })
                {
                    if (name.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
                    {
                        name = name[prefix.Length..];
                        break;
                    }
                }
                name = DecodeEntities(name).Trim();
                if (!string.IsNullOrEmpty(name)) return name;
            }
            if (Captured("<title>(.*?)</title>", html, out var title))
            {
                var cleaned = title
                    .Replace(" - Chaturbate", "", StringComparison.OrdinalIgnoreCase)
                    .Trim();
                if (!string.IsNullOrEmpty(cleaned)) return cleaned;
            }
            return null;
        }

        private static string DecodeEntities(string text)
        {
            return text
                .Replace("&amp;", "&")
                .Replace("&quot;", "\"")
                .Replace("&#39;", "'")
                .Replace("&lt;", "<")
                .Replace("&gt;", ">")
                .Replace("&nbsp;", " ");
        }

        /// <summary>First capture group match, or the whole match when no groups.</summary>
        private static bool Captured(string pattern, string text, out string value, RegexOptions options = RegexOptions.None)
        {
            var match = Regex.Match(text, pattern, options);
            if (!match.Success) { value = ""; return false; }
            value = match.Groups.Count > 1 ? match.Groups[1].Value : match.Value;
            return true;
        }
    }

    // ---- Kick ----

    /// <summary>Channel JSON from the public API. Null only when the channel doesn't exist;
    /// rate-limited / server errors throw so the channel isn't dropped as offline.</summary>
    private static async Task<Dictionary<string, JsonElement>?> FetchKickChannelAsync(string login)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, $"https://kick.com/api/v2/channels/{login}");
        request.Headers.TryAddWithoutValidation("User-Agent", ChromeUserAgent);
        if (KickSession.CookieHeader is { } header)
            request.Headers.TryAddWithoutValidation("Cookie", header);

        var response = await Http.SendAsync(request);
        if (response.StatusCode == System.Net.HttpStatusCode.NotFound) return null;
        if (response.StatusCode != System.Net.HttpStatusCode.OK)
            throw new HttpRequestException($"Kick API returned {(int)response.StatusCode}");

        var json = await response.Content.ReadAsStringAsync();
        using var doc = JsonDocument.Parse(json);
        var dict = new Dictionary<string, JsonElement>();
        foreach (var prop in doc.RootElement.EnumerateObject()) dict[prop.Name] = prop.Value;
        return dict;
    }

    internal static class KickParse
    {
        public static string ProfilePic(Dictionary<string, JsonElement> json)
            => JsonString(json, "user", "profile_pic") ?? "";

        public static string? DisplayName(Dictionary<string, JsonElement> json)
        {
            var name = JsonString(json, "user", "username");
            if (!string.IsNullOrEmpty(name)) return name;
            var slug = JsonString(json, "slug");
            return string.IsNullOrEmpty(slug) ? null : slug;
        }

        public static string CategoryName(JsonElement live)
        {
            if (!live.TryGetProperty("categories", out var categories) ||
                categories.ValueKind != JsonValueKind.Array ||
                categories.GetArrayLength() == 0) return "";
            var first = categories[0];
            return first.TryGetProperty("name", out var name) ? name.GetString() ?? "" : "";
        }

        private static string? JsonString(Dictionary<string, JsonElement> json, params string[] path)
        {
            JsonElement current = default;
            var found = json.TryGetValue(path[0], out current);
            for (var i = 1; found && i < path.Length; i++)
            {
                found = current.TryGetProperty(path[i], out current);
            }
            if (!found || current.ValueKind != JsonValueKind.String) return null;
            return current.GetString();
        }
    }
}