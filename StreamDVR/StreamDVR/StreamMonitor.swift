import Foundation
import AppKit
import WebKit
import SwiftUI
import UserNotifications

struct RecordingStats: Equatable {
    var fileSize: Int64 = 0
    var transferKBps: Double = 0
    var mediaDuration: TimeInterval = 0
    var resolution: String = ""
    var bitrate: String = ""
}

/// Shows notification banners even while the app is in the foreground.
final class LiveNotificationDelegate: NSObject, UNUserNotificationCenterDelegate {
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}

/// Draws the app icon plus a red count badge on the Dock tile.
/// Used because NSDockTile.badgeLabel isn't rendered for this bundle id on this system,
/// so we render the badge ourselves (same approach as DSFDockTile).
final class DockBadgeView: NSView {
    var count: Int = 0 {
        didSet { needsDisplay = true }
    }

    override var isFlipped: Bool { false }

    override func draw(_ dirtyRect: NSRect) {
        let b = bounds
        if let icon = NSApp.applicationIconImage {
            let s = b.size
            let iconSize = icon.size
            let scale = min(s.width / max(iconSize.width, 1), s.height / max(iconSize.height, 1))
            let dw = iconSize.width * scale
            let dh = iconSize.height * scale
            let drect = NSRect(x: (s.width - dw) / 2, y: (s.height - dh) / 2, width: dw, height: dh)
            icon.draw(in: drect, from: .zero, operation: .sourceOver, fraction: 1)
        } else {
            NSColor.windowBackgroundColor.setFill()
            b.fill()
        }

        guard count > 0 else { return }

        let text = "\(count)"
        let fontSize = b.height * 0.40
        let font = NSFont.boldSystemFont(ofSize: fontSize)
        let attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: NSColor.white]
        let size = text.size(withAttributes: attrs)
        let padX = b.height * 0.12
        let padY = b.height * 0.07
        let badgeW = max(size.width + padX * 2, b.height * 0.40)
        let badgeH = fontSize + padY * 2
        let margin = b.width * 0.02
        let badgeRect = NSRect(x: b.maxX - badgeW - margin,
                               y: b.maxY - badgeH - margin,
                               width: badgeW,
                               height: badgeH)
        NSColor.systemRed.setFill()
        NSBezierPath(roundedRect: badgeRect, xRadius: badgeH / 2, yRadius: badgeH / 2).fill()

        let tp = NSPoint(x: badgeRect.midX - size.width / 2,
                         y: badgeRect.midY - size.height / 2)
        (text as NSString).draw(at: tp, withAttributes: attrs)
    }
}

@MainActor
class StreamMonitor: ObservableObject {
    @Published var channels: [StreamChannel] = []
    @Published var selectedChannel: StreamChannel?
    @Published var recordingStatuses: [String: RecordingStatus] = [:]
    @Published var recordingInfo: [String: RecordingStats] = [:]
    @Published var outputDirectory: String = defaultOutputDirectory()
    @Published var isMonitoring = false
    @Published var logs: [LogEntry] = []
    @Published var isLoggedIn = false
    @Published var loggedInUsername: String = ""
    @Published var showLoginView = false
    @Published var isKickLoggedIn = KickSession.isLoggedIn
    @Published var kickUsername = ""
    @Published var showKickLogin = false
    @Published var preventSleep: Bool = (ConfigStore.load(key: "prevent_sleep") ?? "1") == "1"
    @Published var autoSortLive: Bool = (ConfigStore.load(key: "auto_sort_live") ?? "0") == "1"
    @Published var liveNotifications: Bool = (ConfigStore.load(key: "live_notifications") ?? "1") == "1"
    @Published var checkUpdates: Bool = (ConfigStore.load(key: "check_updates") ?? "1") == "1"
    @Published var updateState: UpdateCheckState = .idle
    @Published var installingDependencies = false

    /// Channels to display: when auto-sort is on, currently-recording channels
    /// first, then live (but not recording), then the rest (stable — relative
    /// order within each group is preserved) without overwriting the user's
    /// manually-saved order.
    var sortedChannels: [StreamChannel] {
        guard autoSortLive else { return channels }
        let recording = channels.filter { recorders[$0.id] != nil }
        let live = channels.filter { recorders[$0.id] == nil && !$0.currentStreamTitle.isEmpty }
        let offline = channels.filter { recorders[$0.id] == nil && $0.currentStreamTitle.isEmpty }
        return recording + live + offline
    }

    private var statusTask: Task<Void, Never>?
    private var recorders: [String: StreamRecorder] = [:]
    private var dockBadgeView: DockBadgeView?
    @Published var pollInterval: TimeInterval = 60
    private let sleepPreventer = SleepPreventer()
    private let notificationDelegate = LiveNotificationDelegate()

    /// Channels seen live on the last status poll — used to detect
    /// offline → live transitions for notifications.
    private var knownLiveKeys: Set<String> = []
    private var hasCompletedInitialLiveCheck = false

    struct LogEntry: Identifiable {
        let id = UUID()
        let timestamp = Date()
        let message: String
        let level: Level

        enum Level { case info, warning, error, success }

        var icon: String {
            switch level {
            case .info: return "ℹ️"
            case .warning: return "⚠️"
            case .error: return "❌"
            case .success: return "✅"
            }
        }
    }

    static func defaultOutputDirectory() -> String {
        let paths = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)
        let dir = paths[0].appendingPathComponent("StreamDVR")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.path
    }

    static func provider(for platform: StreamPlatform) -> ChannelProvider {
        switch platform {
        case .twitch: return TwitchProvider()
        case .chaturbate: return ChaturbateProvider()
        case .kick: return KickProvider()
        }
    }

    func watchURL(for channel: StreamChannel) -> String {
        Self.provider(for: channel.platform).watchURL(channel.login)
    }

    func recordingDirectory(for channel: StreamChannel) -> String {
        let base = outputDirectory as NSString
        let platformDir = base.appendingPathComponent(channel.platform.rawValue)
        return (platformDir as NSString).appendingPathComponent(channel.login)
    }

    func openRecordingFolder(for channel: StreamChannel) {
        let dir = recordingDirectory(for: channel)
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        NSWorkspace.shared.open(URL(fileURLWithPath: dir))
    }

    init() {
        loadChannels()
        if let dir = ConfigStore.load(key: "twitch_output_dir") {
            outputDirectory = dir
        }
        Task {
            let api = TwitchAPI.shared
            isLoggedIn = await api.isLoggedIn()
            loggedInUsername = await api.getUsername() ?? ""
        }
        refreshKickUsername()

        let savedInterval = ConfigStore.load(key: "poll_interval").flatMap(TimeInterval.init) ?? 60
        pollInterval = savedInterval > 0 ? savedInterval : 60

        if checkUpdates {
            checkForUpdate()
        }

        installingDependencies = true
        Task.detached(priority: .userInitiated) { [weak self] in
            DependencyInstaller.ensureAll { msg, level in
                let entryLevel: LogEntry.Level
                switch level {
                case .info: entryLevel = .info
                case .warning: entryLevel = .warning
                case .error: entryLevel = .error
                case .success: entryLevel = .success
                }
                DispatchQueue.main.async {
                    self?.addLog(msg, level: entryLevel)
                }
            }
            await MainActor.run { self?.installingDependencies = false }
        }

        // Always refresh channel status (online/offline + title) — immediately
        // at launch and then every pollInterval, regardless of monitoring.
        restartStatusTask()

        // Live-channel notifications.
        UNUserNotificationCenter.current().delegate = notificationDelegate
        if liveNotifications {
            ensureNotificationsAuthorized()
        }
    }

    /// Requests notification permission only when the OS hasn't decided yet;
    /// logs a hint if the user disabled notifications for the app.
    func ensureNotificationsAuthorized() {
        UNUserNotificationCenter.current().getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .notDetermined:
                UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, _ in
                    if !granted {
                        Task { @MainActor in
                            self.addLog("Live notifications disabled", level: .info)
                        }
                    }
                }
            case .denied:
                Task { @MainActor in
                    self.addLog("Live notifications blocked — enable in System Settings > Notifications", level: .warning)
                }
            default:
                break
            }
        }
    }

    func saveLiveNotifications() {
        ConfigStore.save(key: "live_notifications", value: liveNotifications ? "1" : "0")
        if liveNotifications {
            ensureNotificationsAuthorized()
        }
    }

    /// Posts a system notification that a tracked channel went live.
    private func postOnlineNotification(for channel: StreamChannel) {
        guard liveNotifications else { return }
        let content = UNMutableNotificationContent()
        content.title = channel.displayName
        content.body = channel.currentStreamTitle.isEmpty
            ? "\(channel.platform.displayName) channel is now live"
            : channel.currentStreamTitle
        content.sound = .default
        UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        ) { error in
            if let error {
                Task { @MainActor in
                    self.addLog("Notification failed: \(error.localizedDescription)", level: .warning)
                }
            }
        }
    }

    /// Saves the current poll interval and restarts the poller so it takes
    /// effect immediately.
    func savePollInterval() {
        ConfigStore.save(key: "poll_interval", value: String(Int(pollInterval)))
        restartStatusTask()
    }

    /// Restarts the periodic channel-status poller with the current pollInterval.
    private func restartStatusTask() {
        statusTask?.cancel()
        let interval = max(pollInterval, 5)
        statusTask = Task { [weak self] in
            guard let self = self else { return }
            await self.checkAllChannels()
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000))
                await self.checkAllChannels()
            }
        }
    }

    func addChannel(input: String, defaultPlatform: StreamPlatform = .twitch) {
        let trimmed = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }

        guard let parsed = parseInput(trimmed, defaultPlatform: defaultPlatform) else {
            addLog("Could not parse channel input", level: .warning)
            return
        }

        let platform = parsed.platform
        let login = parsed.login
        let id = "\(platform.rawValue):\(login)"
        guard !channels.contains(where: { $0.id == id }) else {
            addLog("Channel \(login) is already tracked", level: .warning)
            return
        }

        let channel = StreamChannel(id: id, login: login, displayName: login, platform: platform)
        channels.append(channel)
        recordingStatuses[id] = .idle
        saveChannels()

        Task {
            if let resolved = try? await Self.provider(for: platform).resolveChannel(login: login) {
                if let idx = channels.firstIndex(where: { $0.id == id }) {
                    channels[idx] = resolved
                }
                saveChannels()
            }
        }

        addLog("Added \(platform.displayName) channel: \(login)", level: .success)
    }

    /// Detects the platform from the raw input and extracts the username.
    /// Accepts plain names (resolved against `defaultPlatform`) or URLs like
    /// `https://www.twitch.tv/shroud`, `https://pl.chaturbate.com/sweetsweet__baby/`,
    /// `https://kick.com/odablock`. Full URLs always override the default platform.
    private func parseInput(_ raw: String, defaultPlatform: StreamPlatform = .twitch) -> (platform: StreamPlatform, login: String)? {
        let lower = raw.lowercased()
        var platform: StreamPlatform = defaultPlatform
        if lower.contains("chaturbate") {
            platform = .chaturbate
        } else if lower.contains("kick.com") || lower.contains("kick.tv") {
            platform = .kick
        } else if lower.contains("twitch.tv") || lower.contains("twitch.com") {
            platform = .twitch
        }

        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)

        // Drop scheme.
        if lower.contains("://") {
            if let range = s.range(of: "://") {
                s = String(s[s.index(after: range.upperBound)...])
            }
        }

        // Normalize Chaturbate locale subdomains (e.g. pl.chaturbate.com) to chaturbate.com.
        if platform == .chaturbate, let range = s.range(of: "chaturbate.com") {
            s = String(s[s.index(after: range.upperBound)...])
        }

        // Username = last non-empty path segment.
        s = s.trimmingCharacters(in: CharacterSet(charactersIn: "/ "))
        if let slash = s.lastIndex(of: "/") {
            s = String(s[s.index(after: slash)...])
        }
        if let q = s.firstIndex(of: "?") { s = String(s[..<q]) }
        if let h = s.firstIndex(of: "#") { s = String(s[..<h]) }

        let login = s.lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
        guard !login.isEmpty else { return nil }
        return (platform, login)
    }

    func removeChannel(_ channel: StreamChannel) {
        let key = channel.id
        if let recorder = recorders[key] {
            recorder.stop()
            recorders.removeValue(forKey: key)
        }
        channels.removeAll { $0.id == channel.id }
        recordingStatuses.removeValue(forKey: key)
        saveChannels()
        addLog("Removed channel: \(channel.login)", level: .info)
    }

    /// Marks a channel as ignored (excluded from monitoring: status polling,
    /// auto-record and live notifications) or un-ignored. Ignoring a currently
    /// recording channel stops its recording.
    func setIgnored(_ channel: StreamChannel, _ ignored: Bool) {
        guard let idx = channels.firstIndex(where: { $0.id == channel.id }) else { return }
        channels[idx].isIgnored = ignored
        saveChannels()
        if ignored {
            if recorders[channel.id] != nil {
                stopRecording(channel)
            }
            recordingStatuses[channel.id] = .idle
            addLog("Ignored \(channel.login) — excluded from monitoring", level: .warning)
        } else {
            addLog("Monitoring \(channel.login) again", level: .info)
        }
    }

    func startMonitoring() {
        guard !isMonitoring else { return }

        isMonitoring = true
        syncSleepPrevention()
        addLog("Started monitoring \(channels.count) channels", level: .success)
    }

    func stopMonitoring() {
        isMonitoring = false
        syncSleepPrevention()
        addLog("Stopped monitoring", level: .info)
    }

    func savePreventSleep() {
        ConfigStore.save(key: "prevent_sleep", value: preventSleep ? "1" : "0")
        syncSleepPrevention()
    }

    func saveAutoSortLive() {
        ConfigStore.save(key: "auto_sort_live", value: autoSortLive ? "1" : "0")
    }

    func saveUpdatePref() {
        ConfigStore.save(key: "check_updates", value: checkUpdates ? "1" : "0")
        if checkUpdates {
            checkForUpdate()
        }
    }

    func checkForUpdate() {
        guard updateState != .checking else { return }
        updateState = .checking
        Task {
            let current = (Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String)
                ?? (Bundle.main.infoDictionary?["CFBundleVersion"] as? String) ?? "0"
            guard let latest = await UpdateChecker.fetchLatest() else {
                updateState = .error("Could not check for updates")
                return
            }
            if UpdateChecker.compare(latest.version, current) == .orderedDescending {
                updateState = .updateAvailable(latest)
                addLog("Update available: \(latest.version)", level: .info)
            } else {
                updateState = .upToDate(current: current, latest: latest.version)
            }
        }
    }

    /// Downloads the new build, then hands off to a detached installer script
    /// that replaces the bundle once this app has quit and relaunches it.
    func downloadAndInstallUpdate() {
        guard case .updateAvailable(let info) = updateState else { return }
        updateState = .downloading(info)
        Task {
            do {
                let zip = try await UpdateChecker.download(info)
                let tempBase = FileManager.default.temporaryDirectory
                    .appendingPathComponent("StreamDVR-Updater", isDirectory: true)
                try? FileManager.default.removeItem(at: tempBase)
                try FileManager.default.createDirectory(at: tempBase, withIntermediateDirectories: true)

                let ditto = Process()
                ditto.executableURL = URL(fileURLWithPath: "/usr/bin/ditto")
                ditto.arguments = ["-x", "-k", zip.path, tempBase.path]
                ditto.standardOutput = Pipe()
                ditto.standardError = Pipe()
                try ditto.run()
                ditto.waitUntilExit()
                guard ditto.terminationStatus == 0 else {
                    throw NSError(domain: "StreamDVRUpdate", code: 3,
                                  userInfo: [NSLocalizedDescriptionKey: "Archive extraction failed (ditto exit \(ditto.terminationStatus))"])
                }
                try? FileManager.default.removeItem(at: zip)

                guard let newApp = Self.findAppBundle(in: tempBase) else {
                    throw NSError(domain: "StreamDVRUpdate", code: 2,
                                  userInfo: [NSLocalizedDescriptionKey: "Release archive does not contain a .app bundle"])
                }

                let scriptURL = FileManager.default.temporaryDirectory.appendingPathComponent("streamdvr-install.sh")
                let logURL = FileManager.default.temporaryDirectory.appendingPathComponent("streamdvr-install.log")
                try? FileManager.default.removeItem(at: logURL)
                let script = """
                #!/bin/bash
                exec >> "\(logURL.path)" 2>&1
                echo "=== StreamDVR updater $(date '+%F %T') ==="
                echo "updating to v\(info.version) from \(newApp.path)"
                while pgrep -x "StreamDVR" > /dev/null 2>&1; do sleep 0.5; done
                echo "app exited, installing"
                sleep 1
                rm -rf /Applications/StreamDVR.app || { echo "rm /Applications/StreamDVR.app failed ($?)"; exit 1; }
                /usr/bin/ditto "\(newApp.path)" /Applications/StreamDVR.app || { echo "ditto failed ($?)"; exit 1; }
                echo "installed, relaunching"
                /usr/bin/open /Applications/StreamDVR.app
                rm -rf "\(tempBase.path)"
                echo "done"
                """
                try script.write(toFile: scriptURL.path, atomically: true, encoding: .utf8)
                let chmod = Process()
                chmod.executableURL = URL(fileURLWithPath: "/bin/chmod")
                chmod.arguments = ["+x", scriptURL.path]
                try chmod.run()
                chmod.waitUntilExit()

                let launcher = Process()
                launcher.executableURL = URL(fileURLWithPath: "/bin/bash")
                launcher.arguments = [scriptURL.path]
                launcher.standardOutput = Pipe()
                launcher.standardError = Pipe()
                try launcher.run()

                addLog("Update v\(info.version) downloaded — restarting", level: .success)
                updateState = .idle
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
                    NSApp.terminate(nil)
                }
            } catch {
                addLog("Update failed: \(error.localizedDescription)", level: .error)
                updateState = .error("Download failed: \(error.localizedDescription)")
            }
        }
    }

    private static func findAppBundle(in dir: URL) -> URL? {
        let fm = FileManager.default
        if let items = try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil),
           let top = items.first(where: { $0.pathExtension == "app" }) {
            return top
        }
        if let enumerator = fm.enumerator(at: dir, includingPropertiesForKeys: nil) {
            for case let url as URL in enumerator where url.pathExtension == "app" {
                return url
            }
        }
        return nil
    }

    private func syncSleepPrevention() {
        if isMonitoring && preventSleep {
            sleepPreventer.start()
        } else {
            sleepPreventer.stop()
        }
    }

    func startRecording(_ channel: StreamChannel) {
        let key = channel.id
        recordingStatuses[key] = .recording(duration: 0)
        recordingInfo[key] = RecordingStats()
        addLog("Starting recording: \(channel.login) (\(channel.platform.displayName))", level: .success)

        Task {
            let provider = Self.provider(for: channel.platform)
            if let stream = try? await provider.getStreamInfo(login: channel.login) {
                if let idx = channels.firstIndex(where: { $0.id == channel.id }) {
                    channels[idx].currentStreamTitle = stream.title
                    channels[idx].currentGame = stream.game
                    channels[idx].profileImageURL = stream.profileImageURL
                }
                let recorder = StreamRecorder()
                recorders[key] = recorder
                updateDockBadge()

                let url = stream.streamM3U8.isEmpty ? provider.watchURL(channel.login) : stream.streamM3U8

                var accessToken: String?
                if let token = ConfigStore.load(key: "twitch_access_token") {
                    accessToken = token
                }

                // One subfolder per platform, then one per channel:
                //   <outputDirectory>/<platform>/<login>/<file>.ts
                let channelDir = recordingDirectory(for: channel)
                try? FileManager.default.createDirectory(atPath: channelDir, withIntermediateDirectories: true)

                recorder.startRecording(url: url, outputDir: channelDir, channelName: channel.displayName, accessToken: accessToken, platform: channel.platform, streamInfo: stream) { [weak self] output in
                    Task { @MainActor in
                        guard let self = self else { return }
                        if output.hasPrefix("discarded:") {
                            let path = String(output.dropFirst("discarded:".count))
                            self.addLog("Discarded empty recording (stream produced no data): \(path)", level: .warning)
                        } else {
                            self.addLog("Recording saved: \(output)", level: .success)
                        }
                        self.recordingStatuses[key] = .idle
                        self.recorders.removeValue(forKey: key)
                        self.updateDockBadge()
                    }
                }

                startDurationTimer(for: key)
            } else {
                addLog("Stream not live: \(channel.login)", level: .warning)
                recordingStatuses[key] = .idle
            }
        }
    }

    func stopRecording(_ channel: StreamChannel) {
        let key = channel.id
        if let recorder = recorders[key] {
            recorder.stop()
            recorders.removeValue(forKey: key)
        }
        recordingStatuses[key] = .idle
        recordingInfo.removeValue(forKey: key)
        updateDockBadge()
        addLog("Stopped recording: \(channel.login)", level: .info)
    }

    var hasActiveRecordings: Bool {
        recordingStatuses.values.contains { $0.isActive }
    }

    func stopAllRecordings() {
        let keys = Array(recorders.keys)
        guard !keys.isEmpty else { return }
        for key in keys {
            if let channel = channels.first(where: { $0.id == key }) {
                stopRecording(channel)
            } else {
                recorders[key]?.stop()
                recorders.removeValue(forKey: key)
                recordingStatuses[key] = .idle
                recordingInfo.removeValue(forKey: key)
            }
        }
        addLog("Stopped all recordings", level: .info)
        updateDockBadge()
    }

    private func startDurationTimer(for channelKey: String) {
        Task { [weak self] in
            guard let self = self else { return }
            let start = Date()
            var lastProbe = Date(timeIntervalSince1970: 0)
            while self.recorders[channelKey] != nil && !Task.isCancelled {
                let elapsed = Date().timeIntervalSince(start)
                self.recordingStatuses[channelKey] = .recording(duration: elapsed)
                self.updateDockBadge()

                var stats = self.recordingInfo[channelKey] ?? RecordingStats()
                if let path = self.recorders[channelKey]?.outputPath {
                    if let attrs = try? FileManager.default.attributesOfItem(atPath: path) {
                        let newSize = attrs[.size] as? Int64 ?? 0
                        if stats.fileSize > 0 && newSize >= stats.fileSize {
                            stats.transferKBps = Double(newSize - stats.fileSize) / 1024
                        }
                        stats.fileSize = newSize
                    }
                    if Date().timeIntervalSince(lastProbe) >= 5 {
                        lastProbe = Date()
                        let probed = await self.probeFile(path: path)
                        stats.mediaDuration = probed.mediaDuration
                        stats.resolution = probed.resolution
                        stats.bitrate = probed.bitrate
                    }
                }
                self.recordingInfo[channelKey] = stats

                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
            self.recordingInfo.removeValue(forKey: channelKey)
        }
    }

    private struct ProbeResult {
        let mediaDuration: TimeInterval
        let resolution: String
        let bitrate: String
    }

    private func probeFile(path: String) async -> ProbeResult {
        let ffprobe = "/opt/homebrew/bin/ffprobe"
        guard FileManager.default.fileExists(atPath: ffprobe),
              FileManager.default.fileExists(atPath: path) else {
            return ProbeResult(mediaDuration: 0, resolution: "", bitrate: "")
        }

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: ffprobe)
        proc.arguments = ["-v", "quiet", "-print_format", "json", "-show_streams", "-show_format", path]
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = Pipe()

        return await withCheckedContinuation { continuation in
            DispatchQueue.global().async {
                try? proc.run()
                proc.waitUntilExit()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                continuation.resume(returning: Self.parseProbe(data))
            }
        }
    }

    nonisolated private static func parseProbe(_ data: Data) -> ProbeResult {
        guard let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            return ProbeResult(mediaDuration: 0, resolution: "", bitrate: "")
        }

        let streams = json["streams"] as? [[String: Any]] ?? []
        var width = 0
        var height = 0
        var fps = ""
        var formatBitrate = 0.0
        var duration = 0.0

        for stream in streams {
            let type = stream["codec_type"] as? String ?? ""
            if type == "video" {
                let w = stream["width"] as? Int ?? 0
                let h = stream["height"] as? Int ?? 0
                if w * h > width * height {
                    width = w
                    height = h
                }
                if let rate = stream["avg_frame_rate"] as? String {
                    let parts = rate.split(separator: "/")
                    if parts.count == 2, let num = Double(parts[0]), let den = Double(parts[1]), den > 0 {
                        let val = round(num / den)
                        if val > 0 { fps = String(format: "%.0f", val) }
                    }
                }
            }
        }

        if let format = json["format"] as? [String: Any] {
            formatBitrate = Double(format["bit_rate"] as? String ?? "") ?? 0
            duration = Double(format["duration"] as? String ?? "") ?? 0
            if formatBitrate <= 0, let sizeStr = format["size"] as? String, let size = Double(sizeStr), duration > 0 {
                formatBitrate = (size * 8) / duration
            }
        }

        var resolution = ""
        if width > 0 && height > 0 {
            resolution = "\(width)x\(height)"
            if !fps.isEmpty { resolution += " @\(fps)fps" }
        }

        var bitrate = ""
        if formatBitrate > 0 {
            bitrate = formatBitrate >= 1_000_000
                ? String(format: "%.1f Mbit/s", formatBitrate / 1_000_000)
                : String(format: "%.0f Kbit/s", formatBitrate / 1_000)
        }

        return ProbeResult(mediaDuration: duration, resolution: resolution, bitrate: bitrate)
    }

    private func checkAllChannels() async {
        var liveNow = Set<String>()
        for channel in channels {
            guard !Task.isCancelled else { return }
            let key = channel.id
            if channel.isIgnored { continue }
            do {
                let status = try await Self.provider(for: channel.platform).getStatus(login: channel.login)
                if let idx = channels.firstIndex(where: { $0.id == channel.id }) {
                    if let s = status {
                        channels[idx].profileImageURL = s.profileImageURL
                        channels[idx].currentStreamTitle = s.isLive ? s.title : ""
                        channels[idx].currentGame = s.isLive ? s.game : ""
                        if channels[idx].displayName.lowercased().contains("chaturbate - 100%"),
                           let resolved = try? await Self.provider(for: channel.platform).resolveChannel(login: channel.login) {
                            channels[idx].displayName = resolved.displayName
                            saveChannels()
                        }
                    }

                    // Offline → live transition: notify (skipped on the very
                    // first poll so channels already live at launch don't spam).
                    if status?.isLive == true {
                        liveNow.insert(key)
                        if !knownLiveKeys.contains(key), hasCompletedInitialLiveCheck {
                            postOnlineNotification(for: channels[idx])
                        }
                    }
                }

                if let s = status, s.isLive, isMonitoring, recordingStatuses[key]?.isActive != true {
                    addLog("Stream went live: \(channel.login) - starting auto recording", level: .success)
                    startRecording(channel)
                } else if let s = status, !s.isLive, recordingStatuses[key]?.isActive == true {
                    addLog("Stream ended: \(channel.login)", level: .info)
                    recordingStatuses[key] = .idle
                    recordingInfo.removeValue(forKey: key)
                    if let recorder = recorders[key] {
                        recorder.stop()
                        recorders.removeValue(forKey: key)
                        updateDockBadge()
                    }
                }
            } catch {
                // ignore network errors during polling
            }
        }
        hasCompletedInitialLiveCheck = true
        knownLiveKeys = liveNow
    }

    func openLoginView() {
        showLoginView = true
    }

    func openKickLogin() {
        showKickLogin = true
    }

    func completeKickLogin(cookies: [String: String]) {
        KickSession.save(cookies)
        isKickLoggedIn = KickSession.isLoggedIn
        if isKickLoggedIn {
            addLog("Kick session saved", level: .success)
            refreshKickUsername()
        } else {
            addLog("No Kick cookies found — session not saved", level: .warning)
        }
    }

    func logoutKick() {
        KickSession.clear()
        isKickLoggedIn = false
        kickUsername = ""
        HTTPCookieStorage.shared.cookies?
            .filter { $0.domain.contains("kick.com") }
            .forEach { HTTPCookieStorage.shared.deleteCookie($0) }
        WKWebsiteDataStore.default().httpCookieStore.getAllCookies { cookies in
            for cookie in cookies where cookie.domain.contains("kick.com") {
                Task {
                    await WKWebsiteDataStore.default().httpCookieStore.deleteCookie(cookie)
                }
            }
        }
        addLog("Kick session removed", level: .info)
    }

    func refreshKickUsername() {
        Task { [weak self] in
            let name = await KickSession.fetchUsername()
            if let self, self.isKickLoggedIn {
                self.kickUsername = name ?? ""
            }
        }
    }

    func completeWebLogin(token: String, username: String) {
        ConfigStore.save(key: "twitch_access_token", value: token)
        ConfigStore.save(key: "twitch_username", value: username)
        isLoggedIn = true
        loggedInUsername = username
        addLog("Logged in as \(username)", level: .success)
    }

    func logout() {
        ConfigStore.delete(key: "twitch_access_token")
        ConfigStore.delete(key: "twitch_username")
        isLoggedIn = false
        loggedInUsername = ""
        addLog("Logged out", level: .info)
    }

    private func saveChannels() {
        if let data = try? JSONEncoder().encode(channels),
           let json = String(data: data, encoding: .utf8) {
            ConfigStore.save(key: "channels", value: json)
        }
    }

    private func loadChannels() {
        if let saved = ConfigStore.load(key: "channels"),
           let data = saved.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([StreamChannel].self, from: data) {
            channels = decoded
        } else if let saved = ConfigStore.load(key: "twitch_channels") {
            // Legacy Twitch-only storage (comma-separated logins).
            let logins = saved.components(separatedBy: ",").filter { !$0.isEmpty }
            channels = logins.map {
                StreamChannel(id: "twitch:\($0)", login: $0, displayName: $0, platform: .twitch)
            }
            saveChannels()
        }
    }

    func exportBackup(to url: URL) -> Bool {
        guard let data = try? JSONSerialization.data(withJSONObject: ConfigStore.loadAll(), options: [.prettyPrinted, .sortedKeys]) else {
            addLog("Backup failed: could not encode settings", level: .error)
            return false
        }
        do {
            try data.write(to: url)
            addLog("Exported settings and channel list to \(url.lastPathComponent)", level: .success)
            return true
        } catch {
            addLog("Backup failed: \(error.localizedDescription)", level: .error)
            return false
        }
    }

    @discardableResult
    func importBackup(from url: URL) -> Bool {
        guard let data = try? Data(contentsOf: url),
              let dict = try? JSONDecoder().decode([String: String].self, from: data) else {
            addLog("Restore failed: not a valid backup file", level: .error)
            return false
        }
        ConfigStore.replaceAll(with: dict)
        loadChannels()
        if let dir = ConfigStore.load(key: "twitch_output_dir") {
            outputDirectory = dir
        }
        let savedInterval = ConfigStore.load(key: "poll_interval").flatMap(TimeInterval.init) ?? 60
        pollInterval = savedInterval > 0 ? savedInterval : 60
        preventSleep = (ConfigStore.load(key: "prevent_sleep") ?? "1") == "1"
        autoSortLive = (ConfigStore.load(key: "auto_sort_live") ?? "0") == "1"
        liveNotifications = (ConfigStore.load(key: "live_notifications") ?? "1") == "1"
        checkUpdates = (ConfigStore.load(key: "check_updates") ?? "1") == "1"
        isKickLoggedIn = KickSession.isLoggedIn
        refreshKickUsername()
        Task {
            await TwitchAPI.shared.reloadTokenFromStore()
            isLoggedIn = await TwitchAPI.shared.isLoggedIn()
            loggedInUsername = await TwitchAPI.shared.getUsername() ?? ""
        }
        addLog("Restored settings and channel list from backup", level: .success)
        return true
    }

    func saveChannelOrder() {
        saveChannels()
    }

    private func updateDockBadge() {
        let count = recorders.keys.count
        let tile = NSApp.dockTile

        if dockBadgeView == nil {
            let view = DockBadgeView(frame: CGRect(origin: .zero, size: tile.size))
            tile.contentView = view
            dockBadgeView = view
        }
        dockBadgeView?.frame = CGRect(origin: .zero, size: tile.size)
        dockBadgeView?.count = count
        tile.display()
    }

    func addLog(_ message: String, level: LogEntry.Level = .info) {
        logs.insert(LogEntry(message: message, level: level), at: 0)
        if logs.count > 200 { logs = Array(logs.prefix(200)) }
    }
}
