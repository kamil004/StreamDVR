using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Reflection;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using Microsoft.Win32;
using StreamDVR.Core;

namespace StreamDVR.App;

public partial class MainWindow : Window
{
    StreamMonitor Monitor => App.Monitor!;

    Point _dragStart;
    bool _isDragging;

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
            if (e.PropertyName is nameof(StreamMonitor.HasUpdate))
                UpdateUpdateButton();
            if (e.PropertyName is nameof(StreamMonitor.UpdateState))
                UpdateUpdateStatus();
        };
    }

    void SetVersionTitle()
    {
        var version = Assembly.GetExecutingAssembly().GetName().Version;
        Title = version == null ? "StreamDVR" : $"StreamDVR {version.Major}.{version.Minor}.{version.Build}";
        VersionText!.Text = version == null ? "Version" : $"✓ Version {version.Major}.{version.Minor}.{version.Build}";
        InitPollIntervalPicker();
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
            SignInButton!.Content = "Log out of Twitch";
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

    void UpdateUpdateButton()
    {
        UpdateButton.Visibility = Monitor.HasUpdate ? Visibility.Visible : Visibility.Collapsed;
    }

    void UpdateUpdateStatus()
    {
        UpdateStatusText!.Text = Monitor.UpdateStatusText;
        DownloadUpdateButton.Visibility = Monitor.HasUpdate ? Visibility.Visible : Visibility.Collapsed;
    }

    StreamPlatform SelectedPlatform()
    {
        if (PlatformPicker?.SelectedItem is ComboBoxItem item && item.Tag is string tag)
            return StreamPlatformExtensions.FromSlug(tag);
        return StreamPlatform.Twitch;
    }

    void InitPollIntervalPicker()
    {
        if (PollIntervalPicker == null) return;
        foreach (var itm in PollIntervalPicker.Items)
        {
            if (itm is ComboBoxItem cbi && int.TryParse(cbi.Tag as string, out var s) && s == Monitor.PollIntervalSeconds)
            {
                PollIntervalPicker.SelectedItem = itm;
                return;
            }
        }
    }

    void OnPollIntervalChanged(object sender, SelectionChangedEventArgs e)
    {
        if (PollIntervalPicker.SelectedItem is ComboBoxItem item && int.TryParse(item.Tag as string, out var seconds))
            Monitor.PollIntervalSeconds = seconds;
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
        Monitor.AddChannel(text, SelectedPlatform());
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
        if (item.IsActive) Monitor.StartRecording(item.Channel.Id);
        else Monitor.StopRecording(item.Channel.Id);
    }

    void OnRemoveChannel(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        Monitor.RemoveChannel(item);
    }

    void OnIgnoreChannel(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (ChannelItem)fe.DataContext;
        Monitor.SetIgnored(item.Channel.Id, !item.Channel.IsIgnored);
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

    // ---- Update system ----

    void OnCheckForUpdate(object sender, RoutedEventArgs e) => Monitor.CheckForUpdate();
    void OnDownloadUpdate(object sender, RoutedEventArgs e) => Monitor.DownloadAndInstallUpdate();

    // ---- Reveal in Explorer ----

    void OnRevealRecording(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement fe) return;
        var item = (FileItem)fe.DataContext;
        var fullPath = Path.Combine(Monitor.OutputDirectory, item.Name);
        if (File.Exists(fullPath))
        {
            try
            {
                Process.Start(new ProcessStartInfo
                {
                    FileName = "explorer.exe",
                    Arguments = $"/select,\"{fullPath}\"",
                    UseShellExecute = true
                });
            }
            catch
            {
                Monitor.AddLog("Could not reveal file", isError: true);
            }
        }
    }

    // ---- Drag-to-reorder channels ----

    void OnChannelMouseDown(object sender, MouseButtonEventArgs e)
    {
        if (sender is FrameworkElement fe)
        {
            _dragStart = e.GetPosition(null);
            _isDragging = false;
        }
    }

    void OnChannelMouseMove(object sender, MouseEventArgs e)
    {
        if (e.LeftButton != MouseButtonState.Pressed || _isDragging) return;
        if (sender is not FrameworkElement fe) return;
        var pos = e.GetPosition(null);
        if (Math.Abs(pos.X - _dragStart.X) > SystemParameters.MinimumHorizontalDragDistance ||
            Math.Abs(pos.Y - _dragStart.Y) > SystemParameters.MinimumVerticalDragDistance)
        {
            _isDragging = true;
            var item = fe.DataContext as ChannelItem;
            if (item != null)
            {
                var data = new DataObject("ChannelItem", item);
                DragDrop.DoDragDrop(fe, data, DragDropEffects.Move);
            }
            _isDragging = false;
        }
    }

    void OnChannelsDragOver(object sender, DragEventArgs e)
    {
        e.Effects = e.Data.GetDataPresent("ChannelItem") ? DragDropEffects.Move : DragDropEffects.None;
        e.Handled = true;
    }

    void OnChannelsDrop(object sender, DragEventArgs e)
    {
        if (!e.Data.GetDataPresent("ChannelItem")) return;
        var draggedItem = e.Data.GetData("ChannelItem") as ChannelItem;
        if (draggedItem == null) return;

        var targetPos = e.GetPosition(ChannelsList);
        var items = Monitor.Channels;
        var oldIndex = items.IndexOf(draggedItem);
        if (oldIndex < 0) return;

        // Find the item closest to the drop position
        int newIndex = items.Count - 1;
        for (var i = 0; i < items.Count; i++)
        {
            var container = ChannelsList.ItemContainerGenerator.ContainerFromIndex(i) as FrameworkElement;
            if (container == null) continue;
            var bounds = VisualTreeHelper.GetDescendantBounds(container);
            var point = container.TransformToAncestor(ChannelsList).Transform(new Point(0, 0));
            if (targetPos.Y < point.Y + bounds.Height / 2)
            {
                newIndex = i;
                break;
            }
        }

        if (oldIndex != newIndex)
        {
            Monitor.ReorderChannel(oldIndex, newIndex);
        }
        e.Handled = true;
    }

    void OnWindowDragOver(object sender, DragEventArgs e)
    {
        e.Effects = e.Data.GetDataPresent(DataFormats.FileDrop) ? DragDropEffects.Copy : DragDropEffects.None;
        e.Handled = true;
    }

    void OnWindowDrop(object sender, DragEventArgs e)
    {
        if (!e.Data.GetDataPresent(DataFormats.FileDrop)) return;
        var files = e.Data.GetData(DataFormats.FileDrop) as string[];
        if (files == null) return;
        foreach (var file in files)
        {
            if (Uri.TryCreate(file, UriKind.Absolute, out var uri))
                Monitor.AddChannel(uri.ToString(), SelectedPlatform());
            else
                Monitor.AddChannel(Path.GetFileNameWithoutExtension(file), SelectedPlatform());
        }
    }
}
