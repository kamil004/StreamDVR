using System.Globalization;
using System.Windows;
using System.Windows.Data;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using TwitchDVR.Core;

namespace TwitchDVR.App;

public class BoolToVisibilityConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => (value is true) ? Visibility.Visible : Visibility.Collapsed;

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class BoolRedConverter : IValueConverter
{
    private static readonly SolidColorBrush Red = new(Colors.Red);
    private static readonly SolidColorBrush Gray = new(Colors.Gray);

    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => (value is true) ? Red : Gray;

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class BoolToRecordButtonTextConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => (value is true) ? "Stop" : "Record";

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class ErrorRedConverter : IValueConverter
{
    private static readonly SolidColorBrush Red = new(Colors.Red);
    private static readonly SolidColorBrush Black = new(Colors.Black);

    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => (value is true) ? Red : Black;

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class InitialConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
    {
        var s = value as string;
        return string.IsNullOrEmpty(s) ? "?" : s.Substring(0, 1).ToUpperInvariant();
    }

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class EmptyToImageSourceConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
    {
        var url = value as string;
        if (string.IsNullOrWhiteSpace(url)) return null!;
        try { return new BitmapImage(new Uri(url)); }
        catch { return null!; }
    }

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class TitleToStatusBrushConverter : IValueConverter
{
    private static readonly SolidColorBrush Live = new(Color.FromRgb(0x2E, 0xA0, 0x43));
    private static readonly SolidColorBrush Offline = new(Colors.Gray);

    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => string.IsNullOrEmpty(value as string) ? Offline : Live;

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class TitleToStatusTextConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => string.IsNullOrEmpty(value as string) ? "Offline" : "Online";

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class PlatformNameConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => value is StreamPlatform p ? p.DisplayName() : "?";

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class PlatformColorConverter : IValueConverter
{
    private static readonly SolidColorBrush Twitch = new(Color.FromRgb(0x91, 0x46, 0xFF));
    private static readonly SolidColorBrush Chaturbate = new(Color.FromRgb(0xC4, 0x14, 0x5F));
    private static readonly SolidColorBrush Kick = new(Color.FromRgb(0x2E, 0xA0, 0x43));
    private static readonly SolidColorBrush DefaultBrush = new(Colors.Gray);

    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => value is StreamPlatform p
            ? p switch
            {
                StreamPlatform.Chaturbate => Chaturbate,
                StreamPlatform.Kick => Kick,
                _ => Twitch
            }
            : DefaultBrush;

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class SuccessGreenConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
        => (value is true) ? new SolidColorBrush(Color.FromRgb(0x2E, 0xA0, 0x43)) : new SolidColorBrush(Colors.Black);

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}

public class LogForegroundConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, CultureInfo culture)
    {
        if (value is not LogEntry entry) return Colors.Black;
        if (entry.IsError) return new SolidColorBrush(Colors.Red);
        if (entry.IsSuccess) return new SolidColorBrush(Color.FromRgb(0x2E, 0xA0, 0x43));
        if (entry.IsWarning) return new SolidColorBrush(Color.FromRgb(0xD4, 0x8A, 0x00));
        return new SolidColorBrush(Colors.Black);
    }

    public object ConvertBack(object value, Type targetType, object parameter, CultureInfo culture)
        => throw new NotSupportedException();
}