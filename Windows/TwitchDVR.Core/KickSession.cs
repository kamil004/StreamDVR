using System.Text.Json;

namespace TwitchDVR.Core;

/// <summary>
/// Persisted Kick session (cookie jar) captured from the in-app login WebView.
/// Recording works without it — signing in is an opt-in for ad behavior etc.
/// </summary>
public static class KickSession
{
    public const string StorageKey = "kick_cookies";

    /// <summary>Dedicated client with cookies disabled so only the explicit Cookie header
    /// is sent and responses never populate a shared cookie jar.</summary>
    internal static readonly HttpClient Http = CreateNoCookieClient();

    private static HttpClient CreateNoCookieClient()
    {
        var handler = new HttpClientHandler { UseCookies = false };
        return new HttpClient(handler) { Timeout = TimeSpan.FromSeconds(30) };
    }

    public static Dictionary<string, string> Cookies
    {
        get
        {
            var raw = ConfigStore.Load(StorageKey);
            if (string.IsNullOrEmpty(raw)) return new Dictionary<string, string>();
            try
            {
                return JsonSerializer.Deserialize<Dictionary<string, string>>(raw)
                       ?? new Dictionary<string, string>();
            }
            catch
            {
                return new Dictionary<string, string>();
            }
        }
    }

    public static bool IsLoggedIn => Cookies.Count > 0;

    public static string? CookieHeader
    {
        get
        {
            var parts = Cookies.Select(kv => $"{kv.Key}={kv.Value}").ToList();
            return parts.Count == 0 ? null : string.Join("; ", parts);
        }
    }

    public static void Save(Dictionary<string, string> newCookies)
    {
        if (newCookies.Count == 0) return;
        ConfigStore.Save(StorageKey, JsonSerializer.Serialize(newCookies));
    }

    public static void Clear() => ConfigStore.Delete(StorageKey);

    /// <summary>Fetches the signed-in username (null when not logged in / request fails).</summary>
    public static async Task<string?> FetchUsernameAsync()
    {
        if (!IsLoggedIn) return null;
        using var request = new HttpRequestMessage(HttpMethod.Get, "https://kick.com/api/v2/users/me");
        request.Headers.TryAddWithoutValidation("User-Agent", ChromeUserAgent);
        if (CookieHeader is { } header)
            request.Headers.TryAddWithoutValidation("Cookie", header);

        try
        {
            var response = await Http.SendAsync(request);
            if (response.StatusCode != System.Net.HttpStatusCode.OK) return null;
            var json = await response.Content.ReadAsStringAsync();
            using var doc = JsonDocument.Parse(json);
            return doc.RootElement.TryGetProperty("username", out var user) &&
                   user.ValueKind == JsonValueKind.String ? user.GetString() : null;
        }
        catch
        {
            return null;
        }
    }

    private const string ChromeUserAgent =
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36";
}