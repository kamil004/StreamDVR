import SwiftUI

@main
struct StreamDVRApp: App {
    @StateObject private var monitor = StreamMonitor()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(monitor)
                .frame(minWidth: 900, minHeight: 600)
                .onAppear {
                    if !monitor.isLoggedIn {
                        monitor.addLog("Sign in to Twitch (top right) to enable ad-free recording", level: .info)
                    }
                }
        }
        .windowStyle(.titleBar)
        .windowToolbarStyle(.unified)
        .defaultSize(width: 1050, height: 700)

        #if os(macOS)
        Settings {
            SettingsView()
                .environmentObject(monitor)
        }
        #endif
    }
}
