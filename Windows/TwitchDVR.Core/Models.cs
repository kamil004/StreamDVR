using System.ComponentModel;
using System.Runtime.CompilerServices;

namespace TwitchDVR.Core;

public enum StreamPlatform
{
    Twitch,
    Chaturbate,
    Kick
}

public static class StreamPlatformExtensions
{
    /// <summary>Lower-case identifier used for channel ids and output folders.</summary>
    public static string Slug(this StreamPlatform platform) => platform switch
    {
        StreamPlatform.Chaturbate => "chaturbate",
        StreamPlatform.Kick => "kick",
        _ => "twitch"
    };

    public static string DisplayName(this StreamPlatform platform) => platform switch
    {
        StreamPlatform.Twitch => "Twitch",
        StreamPlatform.Chaturbate => "Chaturbate",
        StreamPlatform.Kick => "Kick",
        _ => "Unknown"
    };

    public static StreamPlatform FromSlug(string? slug) => (slug ?? "").ToLowerInvariant() switch
    {
        "chaturbate" => StreamPlatform.Chaturbate,
        "kick" => StreamPlatform.Kick,
        _ => StreamPlatform.Twitch
    };
}

public class StreamChannel : INotifyPropertyChanged
{
    string _id = "";
    public string Id { get => _id; set { _id = value; OnPropertyChanged(); } }

    string _login = "";
    public string Login { get => _login; set { _login = value; OnPropertyChanged(); } }

    string _displayName = "";
    public string DisplayName { get => _displayName; set { _displayName = value; OnPropertyChanged(); } }

    StreamPlatform _platform = StreamPlatform.Twitch;
    public StreamPlatform Platform
    {
        get => _platform;
        set { if (_platform == value) return; _platform = value; OnPropertyChanged(); }
    }

    string _currentStreamTitle = "";
    public string CurrentStreamTitle
    {
        get => _currentStreamTitle;
        set { if (_currentStreamTitle == value) return; _currentStreamTitle = value; OnPropertyChanged(); }
    }

    string _currentGame = "";
    public string CurrentGame
    {
        get => _currentGame;
        set { if (_currentGame == value) return; _currentGame = value; OnPropertyChanged(); }
    }

    string _profileImageUrl = "";
    public string ProfileImageUrl
    {
        get => _profileImageUrl;
        set { if (_profileImageUrl == value) return; _profileImageUrl = value; OnPropertyChanged(); }
    }

    public event PropertyChangedEventHandler? PropertyChanged;
    void OnPropertyChanged([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
}

public class StreamInfo
{
    public string Title { get; set; } = "";
    public string Game { get; set; } = "";
    public int ViewerCount { get; set; }
    public string ThumbnailUrl { get; set; } = "";
    public string StreamM3U8 { get; set; } = "";
    public string? AccessToken { get; set; }
    public string ProfileImageUrl { get; set; } = "";
}

public class ChannelStatus
{
    public string Id { get; set; } = "";
    public string Login { get; set; } = "";
    public string DisplayName { get; set; } = "";
    public string ProfileImageURL { get; set; } = "";
    public bool IsLive { get; set; }
    public string Title { get; set; } = "";
    public string Game { get; set; } = "";
}

public class RecordingStats
{
    public long FileSize { get; set; }
    public double MediaDuration { get; set; }
    public string Resolution { get; set; } = "";
    public string Bitrate { get; set; } = "";
}