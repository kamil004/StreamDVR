import Foundation
import AppKit
import WebKit
import SwiftUI

struct RecordingStats: Equatable {
    var fileSize: Int64 = 0
    var transferKBps: Double = 0
    var mediaDuration: TimeInterval = 0
    var resolution: String = ""
    var bitrate: String = ""
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

    /// Channels to display: when auto-sort is on, live channels first
    /// (stable — relative order within each group is preserved) without
    /// overwriting the user's manually-saved order.
    var sortedChannels: [StreamChannel] {
        guard autoSortLive else { return channels }
        let live = channels.filter { !$0.currentStreamTitle.isEmpty }
        let offline = channels.filter { $0.currentStreamTitle.isEmpty }
        return live + offline
    }

    private var statusTask: Task<Void, Never>?
    private var recorders: [String: StreamRecorder] = [:]
    private var pollInterval: TimeInterval = 60
    private let sleepPreventer = SleepPreventer()

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
        let dir = paths[0].appendingPathComponent("TwitchDVR")
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
        if let dir = ConfigStore.load(key: "twitch_output_dir") {
            outputDirectory = dir
        }
        Task {
            let api = TwitchAPI.shared
            isLoggedIn = await api.isLoggedIn()
            loggedInUsername = await api.getUsername() ?? ""
        }
        refreshKickUsername()

        // Always refresh channel status (online/offline + title) — immediately
        // at launch and then every pollInterval, regardless of monitoring.
        statusTask = Task { [weak self] in
            guard let self = self else { return }
            await self.checkAllChannels()
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(self.pollInterval * 1_000_000_000))
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

                recorder.startRecording(url: url, outputDir: channelDir, accessToken: accessToken, platform: channel.platform, streamInfo: stream) { [weak self] output in
                    Task { @MainActor in
                        self?.addLog("Recording saved: \(output)", level: .success)
                        self?.recordingStatuses[key] = .idle
                        self?.recorders.removeValue(forKey: key)
                        self?.updateDockBadge()
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
        for channel in channels {
            guard !Task.isCancelled else { return }
            let key = channel.id
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

    func saveChannelOrder() {
        saveChannels()
    }

    private func updateDockBadge() {
        let count = recorders.keys.count
        let tile = NSApp.dockTile
        tile.badgeLabel = count > 0 ? "\(count)" : nil
    }

    func addLog(_ message: String, level: LogEntry.Level = .info) {
        logs.insert(LogEntry(message: message, level: level), at: 0)
        if logs.count > 200 { logs = Array(logs.prefix(200)) }
    }
}
