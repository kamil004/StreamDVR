using System.Diagnostics;

namespace TwitchDVR.Core;

/// <summary>
/// Automatically installs missing runtime dependencies (streamlink, ffmpeg/ffprobe)
/// via the Windows Package Manager (winget) on first launch.
/// </summary>
public static class DependencyInstaller
{
    private static readonly string DepsDir = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "TwitchDVR", "deps");

    /// <summary>
    /// Checks for streamlink and ffprobe; attempts winget install for any that are
    /// missing. Returns true when every dependency is available afterwards.
    /// </summary>
    public static async Task<bool> EnsureAllAsync(Action<string>? log = null)
    {
        var allOk = true;

        if (StreamRecorder.FindStreamlink() == null)
        {
            log?.Invoke("streamlink not found — installing via winget...");
            if (await InstallWingetAsync("Streamlink.Streamlink", log))
            {
                log?.Invoke("✓ streamlink installed");
            }
            else
            {
                allOk = false;
                log?.Invoke("✗ streamlink missing. Install manually: winget install Streamlink.Streamlink");
            }
        }

        if (StreamRecorder.FindFfprobe() == null)
        {
            log?.Invoke("ffprobe not found — installing via winget...");
            if (await InstallWingetAsync("Gyan.FFmpeg", log))
            {
                log?.Invoke("✓ ffmpeg installed");
            }
            else
            {
                allOk = false;
                log?.Invoke("✗ ffmpeg missing. Install manually: winget install Gyan.FFmpeg");
            }
        }

        return allOk;
    }

    private static async Task<bool> InstallWingetAsync(string packageId, Action<string>? log)
    {
        try
        {
            var psi = new ProcessStartInfo("winget",
                $"install --id {packageId} --silent --disable-interactivity --accept-package-agreements --accept-source-agreements")
            {
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                UseShellExecute = false,
                CreateNoWindow = true
            };

            using var proc = Process.Start(psi);
            if (proc == null) return false;

            var stdout = await proc.StandardOutput.ReadToEndAsync();
            var stderr = await proc.StandardError.ReadToEndAsync();
            await proc.WaitForExitAsync();

            if (proc.ExitCode == 0) return true;

            log?.Invoke($"winget install {packageId} failed (exit {proc.ExitCode}).");
            var detail = (stderr.Trim() + "\n" + stdout.Trim()).Trim();
            if (detail.Length > 0) log?.Invoke(detail.Length > 300 ? detail[..300] : detail);
            return false;
        }
        catch
        {
            log?.Invoke("winget is not available on this system.");
            return false;
        }
    }
}