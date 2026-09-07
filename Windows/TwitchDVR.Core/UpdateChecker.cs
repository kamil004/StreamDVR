using System.Text.Json;
using System.Text.RegularExpressions;

namespace TwitchDVR.Core;

public record UpdateInfo(string Version, string Tag, Uri AssetUrl, string AssetName);

public enum UpdateCheckState { Idle, Checking, UpToDate, UpdateAvailable, Downloading, Error }

public static class UpdateChecker
{
    public const string RepoOwner = "kamil004";
    public const string RepoName = "StreamDVR";
    public const string AssetPrefix = "StreamDVR-Windows";

    static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(20) };

    public static string StripV(string s)
    {
        while (s.StartsWith('v') || s.StartsWith('-')) s = s[1..];
        return s;
    }

    public static int CompareVersions(string a, string b)
    {
        var av = a.Split('.', StringSplitOptions.RemoveEmptyEntries).Select(s => int.TryParse(s, out var v) ? v : 0).ToArray();
        var bv = b.Split('.', StringSplitOptions.RemoveEmptyEntries).Select(s => int.TryParse(s, out var v) ? v : 0).ToArray();
        for (var i = 0; i < Math.Max(av.Length, bv.Length); i++)
        {
            var x = i < av.Length ? av[i] : 0;
            var y = i < bv.Length ? bv[i] : 0;
            if (x != y) return x.CompareTo(y);
        }
        return 0;
    }

    public static async Task<UpdateInfo?> FetchLatestAsync()
    {
        using var request = new HttpRequestMessage(HttpMethod.Get,
            $"https://api.github.com/repos/{RepoOwner}/{RepoName}/releases");
        request.Headers.TryAddWithoutValidation("User-Agent", "StreamDVR/1.0 (Windows)");
        try
        {
            var response = await Http.SendAsync(request);
            if (!response.IsSuccessStatusCode) return null;
            var json = await response.Content.ReadAsStringAsync();
            using var doc = JsonDocument.Parse(json);
            if (doc.RootElement.ValueKind != JsonValueKind.Array) return null;

            // Newest Windows release asset across ALL releases (mirrors the macOS
            // updater). The version is parsed from the asset name, independent of
            // the tag, so a release that sorts oddly (created_at ordering) can
            // never shadow the real latest version.
            UpdateInfo? best = null;
            var bestVersion = "";
            var bestPublished = DateTime.MinValue;

            foreach (var release in doc.RootElement.EnumerateArray())
            {
                if (!release.TryGetProperty("assets", out var assets)) continue;
                var tag = release.TryGetProperty("tag_name", out var t) ? t.GetString() ?? "" : "";
                var published = release.TryGetProperty("published_at", out var p) && DateTime.TryParse(p.GetString(), out var dt) ? dt : DateTime.MinValue;

                foreach (var asset in assets.EnumerateArray())
                {
                    var name = asset.TryGetProperty("name", out var n) ? n.GetString() ?? "" : "";
                    if (!name.StartsWith(AssetPrefix, StringComparison.OrdinalIgnoreCase) || !name.EndsWith(".zip", StringComparison.OrdinalIgnoreCase)) continue;
                    var urlStr = asset.TryGetProperty("browser_download_url", out var u) ? u.GetString() : null;
                    if (string.IsNullOrEmpty(urlStr) || !Uri.TryCreate(urlStr, UriKind.Absolute, out var url)) continue;

                    var version = StripV(name.Replace(AssetPrefix, "", StringComparison.OrdinalIgnoreCase)
                        .Replace(".zip", "", StringComparison.OrdinalIgnoreCase));

                    if (best == null
                        || CompareVersions(version, bestVersion) > 0
                        || (CompareVersions(version, bestVersion) == 0 && published > bestPublished))
                    {
                        best = new UpdateInfo(version, tag, url, name);
                        bestVersion = version;
                        bestPublished = published;
                    }
                }
            }
            return best;
        }
        catch { return null; }
    }
}
