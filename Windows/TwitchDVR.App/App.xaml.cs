using System.Windows;

namespace TwitchDVR.App;

public partial class App : Application
{
    public static StreamMonitor? Monitor;

    protected override void OnStartup(StartupEventArgs e)
    {
        Monitor = new StreamMonitor();
        base.OnStartup(e);
    }
}