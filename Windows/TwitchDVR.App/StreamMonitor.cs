using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Runtime.CompilerServices;
using System.Text.Json;
using System.Windows;
using TwitchDVR.Core;

namespace TwitchDVR.App;

public class LogEntry
{
    public DateTime Timestamp { get; set; }
    public string Message { get; set; } = "";
    public bool IsError { get; set; }
    public bool IsSuccess { get; set; }
}

public class FileItem
{
    public string Name { get; set; } = "";
    public string SizeText { get; set; } = "";
}

public class ChannelItem : INotifyPropertyChanged
{
    public StreamChannel Channel { get; set; } = new();

    string _statusText = "Idle";
    public string StatusText { get => _statusText; set { _statusText = value; OnPropertyChanged(); } }

    bool _isActive;
    public bool IsActive { get => _isActive; set { _isActive = value; OnPropertyChanged(); } }

    string _statsText = "";
    public string StatsText { get => _statsText; set { _statsText = value; OnPropertyChanged(); } }

    public event PropertyChangedEventHandler? PropertyChanged;
    void OnPropertyChanged([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
}

public class StreamMonitor : INotifyPropertyChanged
{
    public ObservableCollection<ChannelItem> Channels { get; } = new();
    public ObservableCollection<LogEntry> Logs { get; } = new();
    public ObservableCollection<FileItem> Recordings { get; } = new();

    const int PollIntervalSeconds = 60;

    readonly Dictionary<string, StreamRecorderResult> _recorders = new();
    CancellationTokenSource? _cts;

    public StreamMonitor()
    {
        LoadChannels();

        var savedDir = ConfigStore.Load("twitch_output_dir");
        if (!string.IsNullOrEmpty(savedDir)) _outputDirectory = savedDir;

        IsLoggedIn = TwitchApi.IsLoggedIn;
        Username = TwitchApi.Username ?? "";

        IsKickLoggedIn = KickSession.IsLoggedIn;
        if (IsKickLoggedIn) RefreshKickUsername();

        // Always keep statuses fresh: immediately and then every 60 s.
        _cts = new CancellationTokenSource();
        _ = Task.Run(() => PollLoopAsync(_cts.Token));

        // Check for missing runtime deps (streamlink, ffmpeg) and auto-install.
        _ = Task.Run(async () =>
        {
            AddLog("Checking dependencies (streamlink, ffmpeg)...");
            var ok = await DependencyInstaller.EnsureAllAsync(msg => AddLog(msg));
            if (ok) AddLog("All dependencies ready", isSuccess: true);
            else AddLog("Some dependencies missing — recording may not work", isError: true);
        });
    }

    public void RefreshRecordings()
    {
        Recordings.Clear();
        try
        {
            var dir = new DirectoryInfo(OutputDirectory);
            if (!dir.Exists) return;
            var basePath = OutputDirectory.TrimEnd('\\', '/') + Path.DirectorySeparatorChar;
            foreach (var f in dir.EnumerateFiles("*", SearchOption.AllDirectories)
                .Where(f => f.Extension is ".ts" or ".mp4")
                .OrderByDescending(f => f.CreationTime))
            {
                var rel = f.FullName.StartsWith(basePath, StringComparison.OrdinalIgnoreCase)
                    ? f.FullName[basePath.Length..]
                    : f.Name;
                Recordings.Add(new FileItem { Name = rel, SizeText = FormatSize(f.Length) });
            }
        }
        catch
        {
        }
    }

    public void OpenRecordingsFolder()
    {
        try
        {
            var psi = new ProcessStartInfo("explorer.exe", OutputDirectory) { UseShellExecute = true };
            Process.Start(psi);
        }
        catch
        {
            AddLog("Could not open recordings folder", isError: true);
        }
    }

    public void OpenChannelFolder(StreamChannel channel)
    {
        try
        {
            var dir = PlatformProvider.RecordingDirectory(OutputDirectory, channel);
            Directory.CreateDirectory(dir);
            var psi = new ProcessStartInfo("explorer.exe", dir) { UseShellExecute = true };
            Process.Start(psi);
        }
        catch
        {
            AddLog($"Could not open folder for {channel.Login}", isError: true);
        }
    }

    bool _isMonitoring;
    public bool IsMonitoring { get => _isMonitoring; set { _isMonitoring = value; OnPropertyChanged(); } }

    public int ActiveRecordingCount => _recorders.Count;
    public bool HasActiveRecordings => _recorders.Count > 0;

    bool _isLoggedIn;
    public bool IsLoggedIn { get => _isLoggedIn; set { _isLoggedIn = value; OnPropertyChanged(); } }

    string _username = "";
    public string Username { get => _username; set { _username = value; OnPropertyChanged(); } }

    bool _isKickLoggedIn;
    public bool IsKickLoggedIn { get => _isKickLoggedIn; set { _isKickLoggedIn = value; OnPropertyChanged(); } }

    string _kickUsername = "";
    public string KickUsername { get => _kickUsername; set { _kickUsername = value; OnPropertyChanged(); } }

    string _outputDirectory = DefaultOutputDirectory();
    public string OutputDirectory { get => _outputDirectory; set { _outputDirectory = value; OnPropertyChanged(); } }

    string _headerStatus = "Waiting for start";
    public string HeaderStatus { get => _headerStatus; set { _headerStatus = value; OnPropertyChanged(); } }

    public async Task RefreshLoginAsync()
    {
        await System.Windows.Application.Current.Dispatcher.InvokeAsync(() =>
        {
            IsLoggedIn = TwitchApi.IsLoggedIn;
            Username = TwitchApi.Username ?? "";
        });
    }

    public void AddChannel(string rawInput)
    {
        var parsed = PlatformProvider.ParseInput(rawInput);
        if (parsed == null)
        {
            AddLog("Could not parse channel input", isError: true);
            return;
        }

        var (platform, login) = parsed.Value;
        var id = $"{platform.Slug()}:{login}";
        if (Channels.Any(c => c.Channel.Id == id))
        {
            AddLog($"Channel {login} is already tracked", isError: true);
            return;
        }

        var item = new ChannelItem
        {
            Channel = new StreamChannel { Id = id, Login = login, DisplayName = login, Platform = platform }
        };
        Channels.Add(item);
        SaveChannels();
        AddLog($"Added {platform.DisplayName()} channel: {login}", isSuccess: true);

        _ = RefreshChannelAsync(item);
        _ = Task.Run(async () =>
        {
            try
            {
                var resolved = await PlatformProvider.ResolveChannelAsync(platform, login);
                await UiAsync(() =>
                {
                    if (resolved == null) return;
                    item.Channel.DisplayName = resolved.DisplayName;
                    item.Channel.ProfileImageUrl = resolved.ProfileImageUrl;
                });
            }
            catch
            {
            }
        });
    }

    public void RemoveChannel(ChannelItem item)
    {
        StopRecording(item.Channel.Id);
        Channels.Remove(item);
        SaveChannels();
        AddLog($"Removed channel: {item.Channel.Login}");
    }

    public void StartMonitoring()
    {
        if (IsMonitoring) return;
        IsMonitoring = true;
        HeaderStatus = "Monitoring channels...";
        AddLog($"Started monitoring {Channels.Count} channels", isSuccess: true);
    }

    public void StopMonitoring()
    {
        IsMonitoring = false;
        HeaderStatus = "Waiting for start";
        AddLog("Stopped monitoring");
    }

    public void StartRecording(string channelId)
    {
        var channel = Channels.FirstOrDefault(c => c.Channel.Id == channelId)?.Channel;
        if (channel == null) return;

        var item = Channels.First(c => c.Channel.Id == channelId);
        item.IsActive = true;
        item.StatusText = "Recording 00:00:00";
        item.StatsText = "";
        AddLog($"Starting recording: {channel.Login} ({channel.Platform.DisplayName()})", isSuccess: true);

        _ = Task.Run(async () =>
        {
            StreamInfo? stream = null;
            try { stream = await PlatformProvider.GetStreamInfoAsync(channel.Platform, channel.Login); }
            catch { }

            if (stream == null)
            {
                await UiAsync(() =>
                {
                    item.IsActive = false;
                    item.StatusText = "Idle";
                    AddLog($"Stream not live: {channel.Login}", isError: true);
                });
                return;
            }

            await UiAsync(() =>
            {
                channel.CurrentStreamTitle = stream.Title;
                channel.CurrentGame = stream.Game;
                channel.ProfileImageUrl = stream.ProfileImageUrl;
            });

            var outputDir = PlatformProvider.RecordingDirectory(OutputDirectory, channel);
            var url = string.IsNullOrEmpty(stream.StreamM3U8)
                ? PlatformProvider.WatchUrl(channel.Platform, channel.Login)
                : stream.StreamM3U8;

            var result = StreamRecorder.Start(
                url,
                outputDir,
                TwitchApi.AccessToken,
                channel.Platform,
                stream);

            await UiAsync(() =>
            {
                if (!result.Started)
                {
                    item.IsActive = false;
                    item.StatusText = "Idle";
                    AddLog(result.Message, isError: true);
                    return;
                }
                _recorders[channelId] = result;
                OnPropertyChanged(nameof(HasActiveRecordings));
                OnPropertyChanged(nameof(ActiveRecordingCount));
            });

            if (!result.Started) return;

            _ = Task.Run(() => StatsLoopAsync(channelId, item, result.OutputPath));

            try { result.Process?.WaitForExit(); }
            catch { }

            await UiAsync(() =>
            {
                _recorders.Remove(channelId);
                item.IsActive = false;
                item.StatusText = "Idle";
                item.StatsText = "";
                AddLog($"Recording saved: {result.OutputPath}", isSuccess: true);
                OnPropertyChanged(nameof(HasActiveRecordings));
                OnPropertyChanged(nameof(ActiveRecordingCount));
            });
        });
    }

    public void StopRecording(string channelId)
    {
        if (!_recorders.TryGetValue(channelId, out var result)) return;
        _recorders.Remove(channelId);
        StreamRecorder.Stop(result.Process);
        var item = Channels.FirstOrDefault(c => c.Channel.Id == channelId);
        if (item != null)
        {
            item.IsActive = false;
            item.StatusText = "Idle";
            item.StatsText = "";
        }
        AddLog($"Stopped recording: {item?.Channel.Login ?? channelId}");
        OnPropertyChanged(nameof(HasActiveRecordings));
        OnPropertyChanged(nameof(ActiveRecordingCount));
    }

    public void StopAllRecordings()
    {
        var ids = _recorders.Keys.ToList();
        if (ids.Count == 0) return;
        foreach (var id in ids)
        {
            StopRecording(id);
        }
        AddLog($"Stopped all recordings ({ids.Count})");
    }

    async Task StatsLoopAsync(string channelId, ChannelItem item, string outputPath)
    {
        var start = DateTime.UtcNow;
        var lastProbe = DateTime.MinValue;
        var lastSize = 0L;
        var resolution = "";
        var bitrate = "";

        while (true)
        {
            var fileSize = ReadSize(outputPath);
            var transferKbps = (lastSize > 0 && fileSize >= lastSize) ? (fileSize - lastSize) / 1024.0 : 0;
            lastSize = fileSize;

            if ((DateTime.UtcNow - lastProbe).TotalSeconds >= 5)
            {
                lastProbe = DateTime.UtcNow;
                var probe = ProbeFile(outputPath);
                resolution = probe.Resolution;
                bitrate = probe.Bitrate;
            }

            if (!CleanupNeeded(channelId, item)) break;

            await UiAsync(() =>
            {
                var elapsed = DateTime.UtcNow - start;
                item.StatusText = $"Recording {(int)elapsed.TotalHours:00}:{elapsed.Minutes:00}:{elapsed.Seconds:00}";
                item.StatsText = PrettyStats(fileSize, transferKbps, resolution, bitrate);
            });

            await Task.Delay(1000);
        }
    }

    bool CleanupNeeded(string channelId, ChannelItem item)
    {
        // Recording was stopped manually / stream ended.
        return _recorders.ContainsKey(channelId) && Channels.Contains(item);
    }

    static long ReadSize(string path)
    {
        try { var fi = new FileInfo(path); return fi.Exists ? fi.Length : 0; }
        catch { return 0; }
    }

    static string PrettyStats(long size, double transferKbps, string resolution, string bitrate)
    {
        var parts = new List<string>
        {
            FormatSize(size),
            $"{transferKbps:0} KB/s",
            resolution,
            bitrate
        };
        return string.Join(" · ", parts.Where(p => !string.IsNullOrEmpty(p)));
    }

    static string FormatSize(long bytes)
    {
        const double kb = 1024, mb = 1024 * 1024, gb = 1024 * 1024 * 1024;
        if (bytes >= gb) return $"{bytes / gb:0.0} GB";
        if (bytes >= mb) return $"{bytes / mb:0.0} MB";
        if (bytes >= kb) return $"{bytes / kb:0.0} KB";
        return $"{bytes} B";
    }

    static (double Duration, string Resolution, string Bitrate) ProbeFile(string path)
    {
        var ffprobe = StreamRecorder.FindFfprobe();
        if (ffprobe == null) return (0, "", "");

        try
        {
            var psi = new ProcessStartInfo
            {
                FileName = ffprobe,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                UseShellExecute = false,
                CreateNoWindow = true
            };
            psi.ArgumentList.Add("-v");
            psi.ArgumentList.Add("quiet");
            psi.ArgumentList.Add("-print_format");
            psi.ArgumentList.Add("json");
            psi.ArgumentList.Add("-show_streams");
            psi.ArgumentList.Add("-show_format");
            psi.ArgumentList.Add(path);

            using var proc = Process.Start(psi);
            if (proc == null) return (0, "", "");
            var output = proc.StandardOutput.ReadToEnd();
            proc.WaitForExit(5000);

            using var doc = JsonDocument.Parse(output);
            var root = doc.RootElement;

            var width = 0;
            var height = 0;
            var fps = "";
            double bitrate = 0;
            double duration = 0;

            if (root.TryGetProperty("streams", out var streams))
            {
                foreach (var s in streams.EnumerateArray())
                {
                    if ((s.TryGetProperty("codec_type", out var codecType) ? codecType.GetString() : "") != "video") continue;

                    var w = s.TryGetProperty("width", out var wJson) ? wJson.GetInt32() : 0;
                    var h = s.TryGetProperty("height", out var hJson) ? hJson.GetInt32() : 0;
                    if (w * h > width * height) { width = w; height = h; }

                    if (s.TryGetProperty("avg_frame_rate", out var rateJson))
                    {
                        var parts = (rateJson.GetString() ?? "/").Split('/');
                        if (parts.Length == 2 && double.TryParse(parts[0], out var num) && double.TryParse(parts[1], out var den) && den > 0)
                        {
                            var val = Math.Round(num / den);
                            if (val > 0) fps = val.ToString("0");
                        }
                    }
                }
            }

            if (root.TryGetProperty("format", out var format))
            {
                if (format.TryGetProperty("bit_rate", out var brJson) && double.TryParse(brJson.GetString(), out var br))
                    bitrate = br;
                if (format.TryGetProperty("duration", out var durJson) && double.TryParse(durJson.GetString(), out var dur))
                    duration = dur;
                if (bitrate <= 0)
                {
                    if (format.TryGetProperty("size", out var sizeJson) && double.TryParse(sizeJson.GetString(), out var size) && duration > 0)
                        bitrate = size * 8 / duration;
                }
            }

            var resolution = width > 0 ? $"{width}x{height}" + (fps.Length > 0 ? $" @{fps}fps" : "") : "";
            var bitrateText = bitrate > 0
                ? (bitrate >= 1_000_000 ? $"{bitrate / 1_000_000:0.0} Mbit/s" : $"{bitrate / 1_000:0} Kbit/s")
                : "";

            return (duration, resolution, bitrateText);
        }
        catch
        {
            return (0, "", "");
        }
    }

    async Task PollLoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            foreach (var item in Channels.ToList())
            {
                if (ct.IsCancellationRequested) return;
                await RefreshChannelAsync(item);
            }
            try { await Task.Delay(TimeSpan.FromSeconds(PollIntervalSeconds), ct); }
            catch (TaskCanceledException) { return; }
        }
    }

    async Task RefreshChannelAsync(ChannelItem item)
    {
        var channel = item.Channel;
        try
        {
            var status = await PlatformProvider.GetStatusAsync(channel.Platform, channel.Login);
            await UiAsync(() =>
            {
                if (status == null) return;
                item.Channel.ProfileImageUrl = status.ProfileImageURL;
                item.Channel.DisplayName = status.DisplayName;
                item.Channel.CurrentStreamTitle = status.IsLive ? status.Title : "";
                item.Channel.CurrentGame = status.IsLive ? status.Game : "";
            });

            if (status != null && status.IsLive && IsMonitoring && !_recorders.ContainsKey(channel.Id))
            {
                AddLog($"Stream went live: {channel.Login} - starting auto recording", isSuccess: true);
                StartRecording(channel.Id);
            }
            else if (status != null && !status.IsLive && _recorders.ContainsKey(channel.Id))
            {
                AddLog($"Stream ended: {channel.Login}");
                await UiAsync(() =>
                {
                    StreamRecorder.Stop(_recorders[channel.Id].Process);
                    _recorders.Remove(channel.Id);
                    item.IsActive = false;
                    item.StatusText = "Idle";
                    item.StatsText = "";
                    OnPropertyChanged(nameof(HasActiveRecordings));
                    OnPropertyChanged(nameof(ActiveRecordingCount));
                });
            }
        }
        catch
        {
            // ignore network errors during polling
        }
    }

    void LoadChannels()
    {
        var saved = ConfigStore.Load("channels");
        if (!string.IsNullOrEmpty(saved))
        {
            try
            {
                using var doc = JsonDocument.Parse(saved);
                foreach (var el in doc.RootElement.EnumerateArray())
                {
                    var login = JsonString(el, "login");
                    if (string.IsNullOrEmpty(login)) continue;
                    var platform = StreamPlatformExtensions.FromSlug(JsonString(el, "platform"));
                    var id = JsonString(el, "id") ?? $"{platform.Slug()}:{login}";
                    var display = JsonString(el, "displayName") ?? login;
                    Channels.Add(new ChannelItem
                    {
                        Channel = new StreamChannel
                        {
                            Id = id,
                            Login = login,
                            DisplayName = display,
                            Platform = platform
                        }
                    });
                }
                return;
            }
            catch
            {
            }
        }

        // Legacy Twitch-only storage (comma-separated logins).
        var legacy = ConfigStore.Load("twitch_channels");
        if (string.IsNullOrEmpty(legacy)) return;
        foreach (var login in legacy.Split(',', StringSplitOptions.RemoveEmptyEntries))
        {
            var l = login.Trim();
            Channels.Add(new ChannelItem
            {
                Channel = new StreamChannel
                {
                    Id = $"twitch:{l}",
                    Login = l,
                    DisplayName = l,
                    Platform = StreamPlatform.Twitch
                }
            });
        }
    }

    void SaveChannels()
    {
        var payload = Channels.Select(c => new
        {
            id = c.Channel.Id,
            login = c.Channel.Login,
            displayName = c.Channel.DisplayName,
            platform = c.Channel.Platform.Slug()
        }).ToList();
        ConfigStore.Save("channels", JsonSerializer.Serialize(payload));
    }

    static string? JsonString(JsonElement el, string name)
    {
        return el.TryGetProperty(name, out var p) && p.ValueKind == JsonValueKind.String ? p.GetString() : null;
    }

    public static string DefaultOutputDirectory()
    {
        var docs = Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments);
        var dir = Path.Combine(docs, "TwitchDVR");
        try { Directory.CreateDirectory(dir); } catch { }
        return dir;
    }

    public void AddLog(string message, bool isError = false, bool isSuccess = false)
    {
        var entry = new LogEntry
        {
            Timestamp = DateTime.Now,
            Message = message,
            IsError = isError,
            IsSuccess = isSuccess
        };
        Application.Current.Dispatcher.BeginInvoke(() =>
        {
            Logs.Insert(0, entry);
            while (Logs.Count > 200) Logs.RemoveAt(Logs.Count - 1);
        });
    }

    static async Task UiAsync(Action action)
    {
        await Application.Current.Dispatcher.InvokeAsync(action);
    }

    // ---- Kick session ----

    public void CompleteKickLogin(Dictionary<string, string> cookies)
    {
        KickSession.Save(cookies);
        IsKickLoggedIn = KickSession.IsLoggedIn;
        if (IsKickLoggedIn)
        {
            AddLog("Kick session saved", isSuccess: true);
            RefreshKickUsername();
        }
        else
        {
            AddLog("No Kick cookies found — session not saved", isError: true);
        }
    }

    public void LogoutKick()
    {
        KickSession.Clear();
        IsKickLoggedIn = false;
        KickUsername = "";
        AddLog("Kick session removed");
    }

    async void RefreshKickUsername()
    {
        var name = await KickSession.FetchUsernameAsync();
        await UiAsync(() =>
        {
            if (IsKickLoggedIn && !string.IsNullOrEmpty(name)) KickUsername = name;
        });
    }

    public event PropertyChangedEventHandler? PropertyChanged;
    void OnPropertyChanged([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
}