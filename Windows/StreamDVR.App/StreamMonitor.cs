using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Net.Http;
using System.Runtime.CompilerServices;
using System.Text.Json;
using System.Windows;
using StreamDVR.Core;

namespace StreamDVR.App;

public class LogEntry
{
    public DateTime Timestamp { get; set; }
    public string Message { get; set; } = "";
    public bool IsError { get; set; }
    public bool IsSuccess { get; set; }
    public bool IsWarning { get; set; }
    public string Icon => IsError ? "✕" : IsSuccess ? "✓" : IsWarning ? "⚠" : "ℹ";
    public System.Windows.Media.Brush Foreground =>
        IsError ? new System.Windows.Media.SolidColorBrush(System.Windows.Media.Colors.Red) :
        IsSuccess ? new System.Windows.Media.SolidColorBrush(System.Windows.Media.Color.FromRgb(0x2E, 0xA0, 0x43)) :
        IsWarning ? new System.Windows.Media.SolidColorBrush(System.Windows.Media.Color.FromRgb(0xD4, 0x8A, 0x00)) :
        new System.Windows.Media.SolidColorBrush(System.Windows.Media.Colors.Black);
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

    const int DefaultPollIntervalSeconds = 60;
    int _pollIntervalSeconds = DefaultPollIntervalSeconds;

    /// How often channels are checked for a live stream (15 / 30 / 60 / 120 s).
    public int PollIntervalSeconds
    {
        get => _pollIntervalSeconds;
        set
        {
            var v = value is 15 or 30 or 60 or 120 ? value : DefaultPollIntervalSeconds;
            if (_pollIntervalSeconds == v) return;
            _pollIntervalSeconds = v;
            ConfigStore.Save("poll_interval", v.ToString());
            OnPropertyChanged();
            RestartPollLoop();
        }
    }

    readonly Dictionary<string, StreamRecorderResult> _recorders = new();
    CancellationTokenSource? _cts;
    static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(30) };

    public StreamMonitor()
    {
        LoadChannels();

        var savedDir = ConfigStore.Load("twitch_output_dir");
        if (!string.IsNullOrEmpty(savedDir)) _outputDirectory = savedDir;

        _autoSortLive = ConfigStore.Load("auto_sort_live") == "1";
        _preventSleep = ConfigStore.Load("prevent_sleep") == "1";
        if (int.TryParse(ConfigStore.Load("poll_interval"), out var poll) && poll is 15 or 30 or 60 or 120)
            _pollIntervalSeconds = poll;

        IsLoggedIn = TwitchApi.IsLoggedIn;
        Username = TwitchApi.Username ?? "";

        IsKickLoggedIn = KickSession.IsLoggedIn;
        if (IsKickLoggedIn) RefreshKickUsername();

        // Always keep statuses fresh: immediately and then every poll interval.
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

        CheckForUpdate();
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

    bool _autoSortLive;
    public bool AutoSortLive { get => _autoSortLive; set { _autoSortLive = value; OnPropertyChanged(); ConfigStore.Save("auto_sort_live", value ? "1" : "0"); } }

    bool _preventSleep;
    public bool PreventSleep { get => _preventSleep; set { _preventSleep = value; OnPropertyChanged(); ConfigStore.Save("prevent_sleep", value ? "1" : "0"); SyncSleepPrevention(); } }

    UpdateCheckState _updateState = UpdateCheckState.Idle;
    public UpdateCheckState UpdateState { get => _updateState; set { _updateState = value; OnPropertyChanged(); OnPropertyChanged(nameof(UpdateStatusText)); OnPropertyChanged(nameof(HasUpdate)); } }

    UpdateInfo? _pendingUpdate;
    public UpdateInfo? PendingUpdate { get => _pendingUpdate; set { _pendingUpdate = value; OnPropertyChanged(); } }

    public bool HasUpdate => UpdateState == UpdateCheckState.UpdateAvailable;
    public string UpdateStatusText => UpdateState switch
    {
        UpdateCheckState.Checking => "Checking...",
        UpdateCheckState.UpToDate => "Up to date",
        UpdateCheckState.UpdateAvailable => $"Update {PendingUpdate?.Version} available",
        UpdateCheckState.Downloading => "Downloading...",
        UpdateCheckState.Error => "Update check failed",
        _ => ""
    };

    int _onlineCount;
    public int OnlineCount { get => _onlineCount; set { _onlineCount = value; OnPropertyChanged(); } }

    public int OfflineCount => Channels.Count - OnlineCount;
    public int IgnoredCount => Channels.Count(c => c.Channel.IsIgnored);
    public int RecordingCount => _recorders.Count;

    public async Task RefreshLoginAsync()
    {
        await System.Windows.Application.Current.Dispatcher.InvokeAsync(() =>
        {
            IsLoggedIn = TwitchApi.IsLoggedIn;
            Username = TwitchApi.Username ?? "";
        });
    }

    public void AddChannel(string rawInput, StreamPlatform defaultPlatform = StreamPlatform.Twitch)
    {
        var parsed = PlatformProvider.ParseInput(rawInput);
        if (parsed == null)
        {
            // No URL detected — use the selected platform from the picker
            var login = rawInput.Trim().ToLowerInvariant().Trim('/');
            if (string.IsNullOrEmpty(login)) { AddLog("Could not parse channel input", isError: true); return; }
            var id = $"{defaultPlatform.Slug()}:{login}";
            if (Channels.Any(c => c.Channel.Id == id))
            {
                AddLog($"Channel {login} is already tracked", isError: true);
                return;
            }
            var item = new ChannelItem
            {
                Channel = new StreamChannel { Id = id, Login = login, DisplayName = login, Platform = defaultPlatform }
            };
            Channels.Add(item);
            SaveChannels();
            AddLog($"Added {defaultPlatform.DisplayName()} channel: {login}", isSuccess: true);
            _ = RefreshChannelAsync(item);
            _ = Task.Run(async () =>
            {
                try
                {
                    var resolved = await PlatformProvider.ResolveChannelAsync(defaultPlatform, login);
                    await UiAsync(() =>
                    {
                        if (resolved == null) return;
                        item.Channel.DisplayName = resolved.DisplayName;
                        item.Channel.ProfileImageUrl = resolved.ProfileImageUrl;
                    });
                }
                catch { }
            });
            return;
        }

        var (platform, loginFromUrl) = parsed.Value;
        var urlId = $"{platform.Slug()}:{loginFromUrl}";
        if (Channels.Any(c => c.Channel.Id == urlId))
        {
            AddLog($"Channel {loginFromUrl} is already tracked", isError: true);
            return;
        }

        var urlItem = new ChannelItem
        {
            Channel = new StreamChannel { Id = urlId, Login = loginFromUrl, DisplayName = loginFromUrl, Platform = platform }
        };
        Channels.Add(urlItem);
        SaveChannels();
        AddLog($"Added {platform.DisplayName()} channel: {loginFromUrl}", isSuccess: true);

        _ = RefreshChannelAsync(urlItem);
        _ = Task.Run(async () =>
        {
            try
            {
                var resolved = await PlatformProvider.ResolveChannelAsync(platform, loginFromUrl);
                await UiAsync(() =>
                {
                    if (resolved == null) return;
                    urlItem.Channel.DisplayName = resolved.DisplayName;
                    urlItem.Channel.ProfileImageUrl = resolved.ProfileImageUrl;
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

    /// Marks a channel as ignored (excluded from monitoring: status polling,
    /// auto-record and live notifications) or un-ignored. Ignoring a currently
    /// recording channel stops its recording.
    public void SetIgnored(string channelId, bool ignored)
    {
        var item = Channels.FirstOrDefault(c => c.Channel.Id == channelId);
        if (item is null) return;
        item.Channel.IsIgnored = ignored;
        SaveChannels();
        if (ignored)
        {
            StopRecording(channelId);
            AddLog($"Ignored {item.Channel.Login} — excluded from monitoring", isWarning: true);
        }
        else
        {
            AddLog($"Monitoring {item.Channel.Login} again", isSuccess: true);
        }
        OnPropertyChanged(nameof(IgnoredCount));
    }

    public void StartMonitoring()
    {
        if (IsMonitoring) return;
        IsMonitoring = true;
        HeaderStatus = "Monitoring channels...";
        SyncSleepPrevention();
        AddLog($"Started monitoring {Channels.Count} channels", isSuccess: true);
    }

    public void StopMonitoring()
    {
        IsMonitoring = false;
        HeaderStatus = "Waiting for start";
        SyncSleepPrevention();
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
        var resolution = "";
        var bitrate = "";

        while (true)
        {
            var fileSize = ReadSize(outputPath);

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
                item.StatsText = PrettyStats(fileSize, resolution, bitrate);
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

    static string PrettyStats(long size, string resolution, string bitrate)
    {
        var parts = new List<string>
        {
            FormatSize(size),
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

    void RestartPollLoop()
    {
        _cts?.Cancel();
        _cts = new CancellationTokenSource();
        _ = Task.Run(() => PollLoopAsync(_cts.Token));
    }

    async Task PollLoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            foreach (var item in Channels.ToList())
            {
                if (ct.IsCancellationRequested) return;
                if (item.Channel.IsIgnored) continue;
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
            var wasLive = !string.IsNullOrEmpty(item.Channel.CurrentStreamTitle);
            var status = await PlatformProvider.GetStatusAsync(channel.Platform, channel.Login);
            await UiAsync(() =>
            {
                if (status == null) return;
                item.Channel.ProfileImageUrl = status.ProfileImageURL;
                item.Channel.DisplayName = status.DisplayName;
                item.Channel.CurrentStreamTitle = status.IsLive ? status.Title : "";
                item.Channel.CurrentGame = status.IsLive ? status.Game : "";

                if (status.IsLive != wasLive && AutoSortLive)
                    SortLiveChannelsToTop();

                OnlineCount = Channels.Count(c => !string.IsNullOrEmpty(c.Channel.CurrentStreamTitle));
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
                    OnPropertyChanged(nameof(RecordingCount));
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
                            Platform = platform,
                            IsIgnored = el.TryGetProperty("isIgnored", out var ig) && ig.ValueKind == JsonValueKind.True
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
            platform = c.Channel.Platform.Slug(),
            isIgnored = c.Channel.IsIgnored
        }).ToList();
        ConfigStore.Save("channels", JsonSerializer.Serialize(payload));
    }

    static bool ChannelIsLive(ChannelItem item) => !string.IsNullOrEmpty(item.Channel.CurrentStreamTitle);

    /// Moves all live channels to the top of the list, keeping their relative order.
    void SortLiveChannelsToTop()
    {
        if (Channels.Count < 2) return;

        var isSorted = true;
        var seenOffline = false;
        foreach (var c in Channels)
        {
            if (ChannelIsLive(c))
            {
                if (seenOffline) { isSorted = false; break; }
            }
            else
            {
                seenOffline = true;
            }
        }
        if (isSorted) return;

        var live = Channels.Where(ChannelIsLive).ToList();
        var offline = Channels.Where(c => !ChannelIsLive(c)).ToList();
        Channels.Clear();
        foreach (var item in live) Channels.Add(item);
        foreach (var item in offline) Channels.Add(item);
        SaveChannels();
    }

    /// Persists a manual reorder (e.g. drag-to-reorder in the UI).
    public void ReorderChannel(int oldIndex, int newIndex)
    {
        if (oldIndex < 0 || oldIndex >= Channels.Count || newIndex < 0 || newIndex >= Channels.Count) return;
        if (oldIndex == newIndex) return;
        Channels.Move(oldIndex, newIndex);
        SaveChannels();
    }

    static string? JsonString(JsonElement el, string name)
    {
        return el.TryGetProperty(name, out var p) && p.ValueKind == JsonValueKind.String ? p.GetString() : null;
    }

    public static string DefaultOutputDirectory()
    {
        var docs = Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments);
        var dir = Path.Combine(docs, "StreamDVR");
        try { Directory.CreateDirectory(dir); } catch { }
        return dir;
    }

    public void AddLog(string message, bool isError = false, bool isSuccess = false, bool isWarning = false)
    {
        var entry = new LogEntry
        {
            Timestamp = DateTime.Now,
            Message = message,
            IsError = isError,
            IsSuccess = isSuccess,
            IsWarning = isWarning
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

    // ---- Sleep prevention ----

    [System.Runtime.InteropServices.DllImport("kernel32.dll")]
    static extern uint SetThreadExecutionState(uint esFlags);

    const uint ES_CONTINUOUS = 0x80000000;
    const uint ES_SYSTEM_REQUIRED = 0x00000001;

    void SyncSleepPrevention()
    {
        if (IsMonitoring && PreventSleep)
            SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED);
        else
            SetThreadExecutionState(ES_CONTINUOUS);
    }

    // ---- Update system ----

    public void CheckForUpdate()
    {
        if (UpdateState == UpdateCheckState.Checking) return;
        UpdateState = UpdateCheckState.Checking;
        _ = Task.Run(async () =>
        {
            var current = System.Reflection.Assembly.GetExecutingAssembly().GetName().Version;
            var currentStr = current != null ? $"{current.Major}.{current.Minor}.{current.Build}" : "0";
            var latest = await UpdateChecker.FetchLatestAsync();
            if (latest == null)
            {
                await UiAsync(() => UpdateState = UpdateCheckState.Error);
                return;
            }
            if (UpdateChecker.CompareVersions(latest.Version, currentStr) > 0)
            {
                await UiAsync(() =>
                {
                    PendingUpdate = latest;
                    UpdateState = UpdateCheckState.UpdateAvailable;
                    AddLog($"Update available: {latest.Version}");
                });
            }
            else
            {
                await UiAsync(() => UpdateState = UpdateCheckState.UpToDate);
            }
        });
    }

    public void DownloadAndInstallUpdate()
    {
        if (PendingUpdate == null || UpdateState != UpdateCheckState.UpdateAvailable) return;
        var info = PendingUpdate;
        UpdateState = UpdateCheckState.Downloading;
        _ = Task.Run(async () =>
        {
            try
            {
                var tempDir = Path.Combine(Path.GetTempPath(), "StreamDVR-Updater");
                if (Directory.Exists(tempDir)) Directory.Delete(tempDir, true);
                Directory.CreateDirectory(tempDir);

                var zipPath = Path.Combine(tempDir, "update.zip");
                using (var response = await Http.GetAsync(info.AssetUrl))
                {
                    response.EnsureSuccessStatusCode();
                    await using var fs = File.Create(zipPath);
                    await response.Content.CopyToAsync(fs);
                }

                System.IO.Compression.ZipFile.ExtractToDirectory(zipPath, tempDir, true);
                File.Delete(zipPath);

                var newExe = Directory.GetFiles(tempDir, "StreamDVR.exe", SearchOption.AllDirectories).FirstOrDefault();
                if (newExe == null)
                {
                    await UiAsync(() => { UpdateState = UpdateCheckState.Error; AddLog("Update archive missing StreamDVR.exe", isError: true); });
                    return;
                }

                var currentExe = Environment.ProcessPath;
                var batPath = Path.Combine(tempDir, "install.bat");
                var bat = $@"@echo off
:loop
tasklist /FI ""IMAGENAME eq StreamDVR.exe"" | find /I ""StreamDVR.exe"" >NUL
if %ERRORLEVEL%==0 (timeout /t 1 >NUL & goto loop)
copy /Y ""{newExe}"" ""{currentExe}""
start """" ""{currentExe}""
del /Q ""{batPath}""
rmdir /S /Q ""{tempDir}""
";
                File.WriteAllText(batPath, bat);

                await UiAsync(() =>
                {
                    AddLog($"Update {info.Version} downloaded — restarting", isSuccess: true);
                    UpdateState = UpdateCheckState.Idle;
                });

                Process.Start(new ProcessStartInfo
                {
                    FileName = batPath,
                    UseShellExecute = true,
                    CreateNoWindow = true
                });

                await Task.Delay(500);
                await UiAsync(() => Application.Current.Shutdown());
            }
            catch (Exception ex)
            {
                await UiAsync(() =>
                {
                    AddLog($"Update failed: {ex.Message}", isError: true);
                    UpdateState = UpdateCheckState.Error;
                });
            }
        });
    }

    public event PropertyChangedEventHandler? PropertyChanged;
    void OnPropertyChanged([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
}