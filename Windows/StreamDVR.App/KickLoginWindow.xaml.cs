using System.Windows;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Core;
using StreamDVR.Core;

namespace StreamDVR.App;

public partial class KickLoginWindow : Window
{
    public StreamMonitor Monitor { get; set; } = null!;
    DispatcherTimer? _timer;
    bool _completed;

    private static readonly string[] BlockedPaths =
    {
        "login", "register", "signup", "forgot", "verify", "password", "auth"
    };

    public KickLoginWindow()
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

        WebView.CoreWebView2.Navigate("https://kick.com/login");

        // Auto-finishes once the user logs in and Kick redirects off the login page.
        _timer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(2) };
        _timer.Tick += OnTimerTick;
        _timer.Start();
    }

    async void OnTimerTick(object? sender, EventArgs e)
    {
        if (_completed || WebView.CoreWebView2 == null) return;
        try
        {
            var path = (WebView.CoreWebView2.Source ?? "").ToLowerInvariant();
            if (BlockedPaths.Any(p => path.Contains(p))) return;

            var cookies = await WebView.CoreWebView2.CookieManager
                            .GetCookiesAsync("https://kick.com");
            if (!cookies.Any(c => c.Domain.Contains("kick.com", StringComparison.OrdinalIgnoreCase))) return;
            await FinishAsync(cookies);
        }
        catch
        {
            // Cookie fetch failed during login; keep trying.
        }
    }

    async void OnSave(object sender, RoutedEventArgs e)
    {
        if (_completed || WebView.CoreWebView2 == null) return;
        try
        {
            var cookies = await WebView.CoreWebView2.CookieManager
                            .GetCookiesAsync("https://kick.com");
            await FinishAsync(cookies);
        }
        catch
        {
        }
    }

    async Task FinishAsync(IReadOnlyList<CoreWebView2Cookie> cookies)
    {
        if (_completed) return;
        var dict = new Dictionary<string, string>();
        foreach (var cookie in cookies)
        {
            if (string.IsNullOrEmpty(cookie.Name) || string.IsNullOrEmpty(cookie.Value)) continue;
            if (cookie.Domain.Contains("kick.com", StringComparison.OrdinalIgnoreCase))
                dict[cookie.Name] = cookie.Value;
        }

        if (dict.Count == 0)
        {
            HintText!.Text = "No Kick session found — sign in first, then try again";
            Monitor.AddLog("No Kick session found yet", isError: true);
            return;
        }

        _completed = true;
        _timer?.Stop();
        HintText!.Text = "Kick session saved...";
        Monitor.CompleteKickLogin(dict);
        await Task.Delay(600);
        Close();
    }

    void OnClose(object sender, RoutedEventArgs e)
    {
        _timer?.Stop();
        Close();
    }
}