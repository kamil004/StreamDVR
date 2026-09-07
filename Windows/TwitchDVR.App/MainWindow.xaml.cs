using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using Microsoft.Win32;
using TwitchDVR.Core;

namespace TwitchDVR.App;

public partial class MainWindow : Window
{
    StreamMonitor Monitor => App.Monitor!;

    public MainWindow()
    {
        InitializeComponent();
        DataContext = Monitor;
        SetVersionTitle();
        UpdateLogins();
        UpdateKickLogin();
        UpdateHeader();
        Monitor.PropertyChanged += (_, e) =>
        {
            if (e.PropertyName is nameof(StreamMonitor.IsMonitoring))
                UpdateHeader();
            if (e.PropertyName is nameof(StreamMonitor.IsLoggedIn))
                UpdateLogins();
            if (e.PropertyName is nameof(StreamMonitor.IsKickLoggedIn) or nameof(StreamMonitor.KickUsername))
                UpdateKickLogin();
            if (e.PropertyName is nameof(StreamMonitor.HasActiveRecordings) or nameof(StreamMonitor.ActiveRecordingCount))
                UpdateTaskbarBadge();
        };
    }

    void SetVersionTitle()
    {
        var version = Assembly.GetExecutingAssembly().GetName().Version;
        Title = version == null ? "TwitchDVR" : $"TwitchDVR {version.Major}.{version.Minor}.{version.Build}";
    }

    void UpdateTaskbarBadge()
    {
        if (TaskbarInfo == null) return;
        if (Monitor.ActiveRecordingCount > 0)
            TaskbarInfo.Overlay = BuildOverlay($"{Monitor.ActiveRecordingCount}");
        else
            TaskbarInfo.Overlay = null;
    }

    static ImageSource BuildOverlay(string count)
    {
        var visual = new DrawingVisual();
        using (var dc = visual.RenderOpen())
        {
            dc.DrawEllipse(new SolidColorBrush(Color.FromArgb(235, 220, 20, 20)), null, new Point(9, 9), 9, 9);
            var text = new FormattedText(
                count,
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                new Typeface("Segoe UI"),
                13,
                Brushes.White);
            dc.DrawText(text, new Point(9 - text.Width / 2, 9 - text.Height / 2));
        }
        return new DrawingImage(visual.Drawing);
    }

    void UpdateLogins()
    {
        if (Monitor.IsLoggedIn)
        {
            LoginButton.Content = "Logout";
            AccountText!.Text = $"Logged in as {Monitor.Username}";
            SignInButton!.Content = "Sign in with a different account";
        }
        else
        {
            LoginButton.Content = "Login";
            AccountText!.Text = "Sign in to Twitch to record streams without ads.";
            SignInButton!.Content = "Sign in with Twitch";
        }
    }

    void UpdateKickLogin()
    {
        if (Monitor.IsKickLoggedIn)
        {
            KickAccountText!.Text = string.IsNullOrEmpty(Monitor.KickUsername)
                ? "Kick session active"
                : $"Logged in as {Monitor.KickUsername}";
            KickSignInButton!.Content = "Log out of Kick";
            KickSignInButton.Background = new SolidColorBrush(Color.FromRgb(0xC0, 0x00, 0x00));
        }
        else
        {
            KickAccountText!.Text = "Optional — Kick streams record without logging in. " +
                                    "Signing in sends your session cookies to Kick (e.g. ad behavior for your account).";
            KickSignInButton!.Content = "Sign in to Kick";
            KickSignInButton.Background = new SolidColorBrush(Color.FromRgb(0x2E, 0xA0, 0x43));
        }
    }

    void UpdateHeader()
    {
        MonitorButton.Content = Monitor.IsMonitoring ? "Stop Monitoring" : "Start Monitoring";
        MonitorButton.Background = Monitor.IsMonitoring
            ? (System.Windows.Media.Brush)new System.Windows.Media.SolidColorBrush(System.Windows.Media.Colors.Red)
            : (System.Windows.Media.Brush)new System.Windows.Media.SolidColorBrush(System.Windows.Media.Color.FromRgb(0x91, 0x46, 0xFF));

        StateText!.Text = Monitor.IsMonitoring
            ? $"Monitoring {Monitor.Channels.Count} channels..."
            : "Waiting for start";
    }

    void OnLoginToggle(object sender, RoutedEventArgs e)
    {
        if (Monitor.IsLoggedIn)
        {
            TwitchApi.SetAccessToken(null);
            TwitchApi.SetUsername(null);
            Monitor.IsLoggedIn = false;
            Monitor.Username = "";
            Monitor.AddLog("Logged out");
        }
        else
        {
            var login = new LoginWindow { Owner = this, Monitor = Monitor };
            login.ShowDialog();
        }
    }

    void OnMonitorToggle(object sender, RoutedEventArgs e)
    {
        if (Monitor.IsMonitoring) Monitor.StopMonitoring();
        else Monitor.StartMonitoring();
    }

    void OnAddChannel(object sender, RoutedEventArgs e)
    {
        var text = ChannelInput!.Text;
        if (string.IsNullOrWhiteSpace(text)) return;
        Monitor.AddChannel(text);
        ChannelInput.Text = "";
    }

    void OnStopAll(object sender, RoutedEventArgs e) => Monitor.StopAllRecordings();

    void OnChannelInputKeyDown(object sender, KeyEventArgs e)
    {
        if (e.Key == Key.Enter) OnAddChannel(sender, e);
    }

    void OnRecordToggle(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        // IsChecked (and thus IsActive) is already true when checked.
        if (item.IsActive) Monitor.StartRecording(item.Channel.Id);
        else Monitor.StopRecording(item.Channel.Id);
    }

    void OnRemoveChannel(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        Monitor.RemoveChannel(item);
    }

    void OnOpenChannelFolder(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        Monitor.OpenChannelFolder(item.Channel);
    }

    void OnOpenChannelName(object sender, MouseButtonEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        OpenInBrowser(item.Channel);
    }

    void OpenInBrowser(StreamChannel channel)
    {
        try
        {
            Process.Start(new ProcessStartInfo
            {
                FileName = PlatformProvider.WatchUrl(channel.Platform, channel.Login),
                UseShellExecute = true
            });
        }
        catch
        {
            Monitor.AddLog("Could not open browser", isError: true);
        }
    }

    void OnTabChanged(object sender, SelectionChangedEventArgs e)
    {
        var tabItem = TabControl.SelectedItem as TabItem;
        if (tabItem?.Header?.ToString() == "Recordings")
        {
            Monitor.RefreshRecordings();
        }
    }

    void OnOpenFolder(object sender, RoutedEventArgs e) => Monitor.OpenRecordingsFolder();

    void OnRefreshRecordings(object sender, RoutedEventArgs e) => Monitor.RefreshRecordings();

    void OnClearLogs(object sender, RoutedEventArgs e) => Monitor.Logs.Clear();

    void OnSignIn(object sender, RoutedEventArgs e)
    {
        var login = new LoginWindow { Owner = this, Monitor = Monitor };
        login.ShowDialog();
    }

    void OnKickSignIn(object sender, RoutedEventArgs e)
    {
        if (Monitor.IsKickLoggedIn)
        {
            Monitor.LogoutKick();
        }
        else
        {
            var login = new KickLoginWindow { Owner = this, Monitor = Monitor };
            login.ShowDialog();
        }
    }

    void OnChooseFolder(object sender, RoutedEventArgs e)
    {
        var dialog = new OpenFolderDialog
        {
            Title = "Choose recordings folder",
            InitialDirectory = Monitor.OutputDirectory
        };
        if (dialog.ShowDialog() == true)
        {
            Monitor.OutputDirectory = dialog.FolderName;
            ConfigStore.Save("twitch_output_dir", Monitor.OutputDirectory);
        }
    }
}