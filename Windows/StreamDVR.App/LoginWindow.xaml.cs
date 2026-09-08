using System.Windows;
using System.Windows.Threading;
using StreamDVR.Core;

namespace StreamDVR.App;

public partial class LoginWindow : Window
{
    public StreamMonitor Monitor { get; set; } = null!;
    DispatcherTimer? _timer;

    public LoginWindow()
    {
        InitializeComponent();
        Loaded += OnLoaded;
    }

    async void OnLoaded(object sender, RoutedEventArgs e)
    {
        try
        {
            await WebView.EnsureCoreWebView2Async();
        }
        catch (Exception ex)
        {
            HintText!.Text = $"WebView2 error: {ex.Message}";
            return;
        }

        WebView.CoreWebView2.Navigate("https://www.twitch.tv/login");

        _timer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(2) };
        _timer.Tick += OnTimerTick;
        _timer.Start();
    }

    async void OnTimerTick(object? sender, EventArgs e)
    {
        if (WebView.CoreWebView2 == null) return;
        try
        {
            var cookies = await WebView.CoreWebView2.CookieManager
                            .GetCookiesAsync("https://www.twitch.tv");
            var tokenCookie = cookies.FirstOrDefault(c => c.Name == "auth-token");
            if (tokenCookie?.Value == null || tokenCookie.Value.Length == 0) return;

            TwitchApi.SetAccessToken(tokenCookie.Value);
            HintText!.Text = "Logged in! Resolving username...";

            // Resolve username via GraphQL using the new token.
            var displayName = await TwitchApi.GetViewer();
            TwitchApi.SetUsername(displayName ?? "TwitchUser");
            Monitor.Username = displayName ?? "TwitchUser";
            Monitor.IsLoggedIn = true;
            Monitor.AddLog($"Logged in as {displayName}", isSuccess: true);

            HintText!.Text = "Login successful — you can close this window.";
            _timer?.Stop();
        }
        catch
        {
            // Cookie fetch failed during login; keep trying.
        }
    }

    void OnClose(object sender, RoutedEventArgs e)
    {
        _timer?.Stop();
        Close();
    }
}