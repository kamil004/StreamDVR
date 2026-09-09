using System.Diagnostics;

namespace StreamDVR.Core;

public class StreamRecorderResult
{
    public bool Started { get; set; }
    public string OutputPath { get; set; } = "";
    public string Message { get; set; } = "";
    public Process? Process { get; set; }
}

/// <summary>
/// Wraps the streamlink process (Windows edition). Finds streamlink/ffprobe
/// on PATH and in the common Streamlink install locations.
/// </summary>
public static class StreamRecorder
{
    private static readonly string[] StreamlinkLocations =
    {
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Programs", "streamlink", "bin", "streamlink.exe"),
        @"C:\Program Files\Streamlink\bin\streamlink.exe",
        @"C:\Program Files (x86)\Streamlink\bin\streamlink.exe",
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Programs", "Streamlink", "bin", "streamlink.exe")
    };

    private static readonly string[] FfprobeLocations =
    {
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Programs", "ffmpeg", "bin", "ffprobe.exe"),
        @"C:\Program Files\ffmpeg\bin\ffprobe.exe",
        @"C:\ffmpeg\bin\ffprobe.exe",
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg", @"ffmpeg-*-build\bin\ffprobe.exe")
    };

    public static string? FindStreamlink()
    {
        foreach (var loc in StreamlinkLocations)
        {
            var expanded = ExpandWildcards(loc);
            if (File.Exists(expanded)) return expanded;
        }
        return FindOnPath("streamlink");
    }

    public static string? FindFfprobe()
    {
        foreach (var loc in FfprobeLocations)
        {
            if (File.Exists(loc)) return loc;
        }
        return FindOnPath("ffprobe");
    }

    /// ffmpeg normally sits in the same bin dir as ffprobe (winget Gyan.FFmpeg).
    public static string? FindFfmpeg()
    {
        foreach (var loc in FfprobeLocations)
        {
            var dir = Path.GetDirectoryName(ExpandWildcards(loc));
            if (dir == null) continue;
            var exe = Path.Combine(dir, "ffmpeg.exe");
            if (File.Exists(exe)) return exe;
        }
        return FindOnPath("ffmpeg");
    }

    /// If the recording's start timestamp is far from zero, remuxes it with
    /// stream copy (fast, lossless) so players begin at 00:00 instead of, e.g.,
    /// ~1h44m (the source stream keeps a large base PTS for LL-HLS/fMP4).
    public static string? ConvertToMp4(string path)
    {
        try
        {
            var info = new FileInfo(path);
            if (!info.Exists || info.Length < 1024) return null;
            if (!path.EndsWith(".ts", StringComparison.OrdinalIgnoreCase)) return null;

            var ffmpeg = FindFfmpeg();
            if (ffmpeg == null) return "error:ffmpeg not found";

            var mp4 = path[..^3] + ".mp4";

            // Preferred: stream copy (fast, no re-encoding); MP4 rebases
            // timestamps so players start at 00:00.
            RunTool(ffmpeg, "-y", "-v", "error", "-i", path, "-map", "0", "-c", "copy", "-movflags", "+faststart", mp4);
            if (File.Exists(mp4) && new FileInfo(mp4).Length > 0)
            {
                File.Delete(path);
                return $"ok:{mp4}";
            }
            try { File.Delete(mp4); } catch { }

            // Fallback: transcode audio only (video stays copied).
            RunTool(ffmpeg, "-y", "-v", "error", "-i", path, "-map", "0", "-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart", mp4);
            if (File.Exists(mp4) && new FileInfo(mp4).Length > 0)
            {
                File.Delete(path);
                return $"ok:{mp4}";
            }
            try { File.Delete(mp4); } catch { }
            return "error:MP4 conversion failed";
        }
        catch { return null; }
    }

    private static string? RunTool(string exe, params string[] args)
    {
        try
        {
            var psi = new ProcessStartInfo(exe)
            {
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                UseShellExecute = false,
                CreateNoWindow = true
            };
            foreach (var a in args) psi.ArgumentList.Add(a);
            using var proc = Process.Start(psi);
            if (proc == null) return null;
            var outText = proc.StandardOutput.ReadToEnd();
            proc.WaitForExit(600_000);
            return outText;
        }
        catch { return null; }
    }

    private static string ExpandWildcards(string path)
    {
        if (!path.Contains('*')) return path;
        var dir = Path.GetDirectoryName(path);
        var pattern = Path.GetFileName(path);
        if (dir == null || !Directory.Exists(dir)) return path;
        var match = Directory.EnumerateDirectories(dir, pattern).FirstOrDefault();
        return match ?? path;
    }

    private static string? FindOnPath(string name)
    {
        try
        {
            var psi = new ProcessStartInfo("where.exe", name)
            {
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                UseShellExecute = false,
                CreateNoWindow = true
            };
            using var proc = Process.Start(psi);
            if (proc == null) return null;
            var outPath = proc.StandardOutput.ReadToEnd().Trim();
            proc.WaitForExit(5000);
            var first = outPath.Split('\n', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries).FirstOrDefault();
            return File.Exists(first) ? first : null;
        }
        catch
        {
            return null;
        }
    }

    public static StreamRecorderResult Start(string url, string outputDir, string channelName, string? accessToken,
        StreamPlatform platform, StreamInfo streamInfo)
    {
        var streamlink = FindStreamlink();
        if (streamlink == null)
        {
            return new StreamRecorderResult
            {
                Message = "Error: streamlink not found. Install with: winget install streamlink"
            };
        }

        var now = DateTime.Now;
        var dateString = now.ToString("yyyy-MM-dd");
        var timeString = now.ToString("HH-mm-ss");

        var safeChannel = SanitizeFilename(channelName);
        var finalChannel = string.IsNullOrEmpty(safeChannel) ? "channel" : safeChannel;

        var safeTitle = SanitizeFilename(streamInfo.Title);
        var finalTitle = string.IsNullOrEmpty(safeTitle) ? "stream" : CapLength(safeTitle, 35);
        var filename = $"{finalChannel}_{dateString}-{timeString}_{finalTitle}.ts";
        var outputPath = Path.Combine(outputDir, filename);

        var args = new List<string>();

        // Twitch-only flags: ad removal + premium ad-free auth via the web token.
        if (platform == StreamPlatform.Twitch)
        {
            args.Add("--twitch-disable-ads");
            if (!string.IsNullOrEmpty(accessToken))
            {
                args.Add("--twitch-api-header");
                args.Add($"Authorization=OAuth {accessToken}");
            }
        }

        // Kick: optional session cookies are forwarded to streamlink so requests are
        // made as the signed-in user (recording still works without them).
        if (platform == StreamPlatform.Kick)
        {
            foreach (var cookie in KickSession.Cookies)
            {
                args.Add("--http-cookie");
                args.Add($"{cookie.Key}={cookie.Value}");
            }
        }

        args.AddRange(new[]
        {
            "-o", outputPath,
            "--force",
            "--stream-segment-attempts", "10",
            "--stream-segment-timeout", "30",
            "--retry-open", "30",
            "--retry-streams", "10",
            "--retry-max", "10",
            "--ringbuffer-size", "32M",
            url,
            "best"
        });

        try
        {
            Directory.CreateDirectory(outputDir);
            var psi = new ProcessStartInfo
            {
                FileName = streamlink,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true
            };
            foreach (var arg in args)
            {
                psi.ArgumentList.Add(arg);
            }

            var proc = Process.Start(psi);
            if (proc == null)
            {
                return new StreamRecorderResult { Message = "Failed to start streamlink" };
            }

            proc.OutputDataReceived += (_, e) => { OnOutput(e.Data); };
            proc.ErrorDataReceived += (_, e) => { OnOutput(e.Data); };
            proc.BeginOutputReadLine();
            proc.BeginErrorReadLine();

            return new StreamRecorderResult
            {
                Started = true,
                OutputPath = outputPath,
                Process = proc,
                Message = $"Recording {finalTitle}"
            };
        }
        catch (Exception ex)
        {
            return new StreamRecorderResult { Message = $"Failed to start streamlink: {ex.Message}" };
        }
    }

    public static void Stop(Process? proc)
    {
        if (proc == null || proc.HasExited) return;
        try { proc.Kill(entireProcessTree: true); } catch { }
    }

    private static void OnOutput(string? data)
    {
        // Intentionally unused for now; UI reads file + ffprobe for stats.
    }

    private static string SanitizeFilename(string title)
    {
        var invalid = Path.GetInvalidFileNameChars();
        var cleaned = new string(title.Select(c => invalid.Contains(c) ? '-' : c).ToArray());
        return cleaned.Trim().TrimEnd('.');
    }

    /// Caps a string to max characters at a grapheme (unicode text element)
    /// boundary so multi-byte characters are never split.
    private static string CapLength(string s, int max)
    {
        if (string.IsNullOrEmpty(s) || s.Length <= max) return s;
        var result = new System.Text.StringBuilder();
        var enumerator = System.Globalization.StringInfo.GetTextElementEnumerator(s);
        while (enumerator.MoveNext() && result.Length < max)
        {
            result.Append(enumerator.GetTextElement());
        }
        return result.ToString().Trim();
    }

    /// Removes a leftover output file that never received real data
    /// (some streams produce a 0-byte / header-only .ts). Returns true if removed.
    public static bool DeleteIfEmpty(string path)
    {
        try
        {
            var fi = new FileInfo(path);
            if (!fi.Exists) return false;
            if (fi.Length >= 1024) return false;
            fi.Delete();
            return true;
        }
        catch
        {
            return false;
        }
    }
}