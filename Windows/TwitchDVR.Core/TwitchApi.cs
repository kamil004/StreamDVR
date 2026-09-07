using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace TwitchDVR.Core;

/// <summary>
/// Twitch GraphQL API client (same public web Client-ID as the macOS version).
/// </summary>
public static class TwitchApi
{
    public const string DefaultClientId = "kimne78kx3ncx6brgo4mv6wki5h1ko";
    private const string GraphQlUrl = "https://gql.twitch.tv/gql";

    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(30) };

    public static string? AccessToken => ConfigStore.Load("twitch_access_token");
    public static string? Username => ConfigStore.Load("twitch_username");
    public static bool IsLoggedIn => !string.IsNullOrEmpty(AccessToken);

    public static void SetAccessToken(string? token)
    {
        if (string.IsNullOrEmpty(token)) ConfigStore.Delete("twitch_access_token");
        else ConfigStore.Save("twitch_access_token", token);
    }

    public static void SetUsername(string? username)
    {
        if (string.IsNullOrEmpty(username)) ConfigStore.Delete("twitch_username");
        else ConfigStore.Save("twitch_username", username);
    }

    private static async Task<string> GraphQl(string query, string? accessToken = null)
    {
        using var request = new HttpRequestMessage(HttpMethod.Post, GraphQlUrl);
        request.Headers.TryAddWithoutValidation("Client-ID", DefaultClientId);
        if (!string.IsNullOrEmpty(accessToken))
        {
            request.Headers.TryAddWithoutValidation("Authorization", $"OAuth {accessToken}");
        }
        request.Content = new StringContent(
            JsonSerializer.Serialize(new { query }),
            Encoding.UTF8,
            "application/json");

        var response = await Http.SendAsync(request);
        response.EnsureSuccessStatusCode();
        return await response.Content.ReadAsStringAsync();
    }

    /// <summary>Resolves the logged-in user from the stored OAuth token.</summary>
    public static async Task<string?> GetViewer()
    {
        var token = AccessToken;
        if (string.IsNullOrEmpty(token)) return null;

        var json = await GraphQl("query { viewer { login displayName } }", token);
        using var doc = JsonDocument.Parse(json);
        var root = doc.RootElement;
        if (!root.TryGetProperty("data", out var data) ||
            !data.TryGetProperty("viewer", out var viewer) ||
            viewer.ValueKind != JsonValueKind.Object) return null;

        return viewer.TryGetProperty("displayName", out var d) ? d.GetString() : null;
    }

    public static async Task<StreamChannel?> GetUser(string login)
    {
        var json = await GraphQl($"query {{ user(login: \"{login}\") {{ id login displayName profileImageURL(width: 300) }} }}");
        using var doc = JsonDocument.Parse(json);
        var root = doc.RootElement;
        if (!root.TryGetProperty("data", out var data) ||
            !data.TryGetProperty("user", out var user) ||
            user.ValueKind != JsonValueKind.Object) return null;

        return new StreamChannel
        {
            Id = user.GetProperty("id").GetString() ?? $"twitch:{login}",
            Login = user.TryGetProperty("login", out var l) ? l.GetString() ?? login : login,
            DisplayName = user.TryGetProperty("displayName", out var d) ? d.GetString() ?? login : login,
            Platform = StreamPlatform.Twitch,
            ProfileImageUrl = user.TryGetProperty("profileImageURL", out var a) ? a.GetString() ?? "" : ""
        };
    }

    public static async Task<StreamInfo?> GetStream(string login)
    {
        var json = await GraphQl($"query {{ user(login: \"{login}\") {{ id stream {{ type title game {{ displayName }} }} profileImageURL(width: 300) }} }}");
        using var doc = JsonDocument.Parse(json);
        var root = doc.RootElement;
        if (!root.TryGetProperty("data", out var data) ||
            !data.TryGetProperty("user", out var user) ||
            !user.TryGetProperty("stream", out var stream) ||
            stream.ValueKind != JsonValueKind.Object) return null;

        if (!stream.TryGetProperty("type", out var type) ||
            !string.Equals(type.GetString(), "live", StringComparison.OrdinalIgnoreCase)) return null;

        return new StreamInfo
        {
            Title = stream.TryGetProperty("title", out var t) ? t.GetString() ?? "" : "",
            Game = stream.TryGetProperty("game", out var game) && game.ValueKind == JsonValueKind.Object
                ? (game.TryGetProperty("displayName", out var g) ? g.GetString() ?? "" : "")
                : "",
            ProfileImageUrl = user.TryGetProperty("profileImageURL", out var a) ? a.GetString() ?? "" : ""
        };
    }

    /// <summary>Channel status including avatar even when offline (mirrors the macOS edition).</summary>
    public static async Task<ChannelStatus?> GetChannelStatus(string login)
    {
        var json = await GraphQl($"query {{ user(login: \"{login}\") {{ id login displayName stream {{ type title game {{ displayName }} }} profileImageURL(width: 300) }} }}");
        using var doc = JsonDocument.Parse(json);
        var root = doc.RootElement;
        if (!root.TryGetProperty("data", out var data) ||
            !data.TryGetProperty("user", out var user) ||
            user.ValueKind != JsonValueKind.Object) return null;

        var id = user.TryGetProperty("id", out var idJson) ? idJson.GetString() ?? login : login;
        var displayName = user.TryGetProperty("displayName", out var dnJson) ? dnJson.GetString() ?? login : login;
        var avatar = user.TryGetProperty("profileImageURL", out var avJson) ? avJson.GetString() ?? "" : "";

        var isLive = false;
        var title = "";
        var game = "";
        if (user.TryGetProperty("stream", out var stream) && stream.ValueKind == JsonValueKind.Object &&
            stream.TryGetProperty("type", out var typeJson) &&
            string.Equals(typeJson.GetString(), "live", StringComparison.OrdinalIgnoreCase))
        {
            isLive = true;
            title = stream.TryGetProperty("title", out var t) ? t.GetString() ?? "" : "";
            game = stream.TryGetProperty("game", out var gameJson) && gameJson.ValueKind == JsonValueKind.Object
                ? (gameJson.TryGetProperty("displayName", out var g) ? g.GetString() ?? "" : "")
                : "";
        }

        return new ChannelStatus
        {
            Id = id,
            Login = login,
            DisplayName = displayName,
            ProfileImageURL = avatar,
            IsLive = isLive,
            Title = title,
            Game = game
        };
    }
}