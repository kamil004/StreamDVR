import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct ContentView: View {
    @EnvironmentObject var monitor: StreamMonitor
    @State private var newChannelInput: String = ""
    @State private var selectedTab = 0

    var body: some View {
        VStack(spacing: 0) {
            HeaderView()

            Picker("", selection: $selectedTab) {
                Text("Channels").tag(0)
                Text("Recordings").tag(1)
                Text("Logs").tag(2)
                Text("Settings").tag(3)
            }
            .pickerStyle(.segmented)
            .padding([.horizontal, .bottom], 12)
            .labelsHidden()

            Divider()

            switch selectedTab {
            case 0: ChannelListView(newChannelInput: $newChannelInput)
            case 1: RecordingsView()
            case 2: LogsView()
            case 3: SettingsView()
            default: ChannelListView(newChannelInput: $newChannelInput)
            }

            Spacer()

            Divider()
            StatusBarView()
        }
        .background(Color(NSColor.windowBackgroundColor))
        .sheet(isPresented: $monitor.showLoginView) {
            TwitchLoginView()
        }
        .sheet(isPresented: $monitor.showKickLogin) {
            KickLoginView()
        }
    }
}

struct HeaderView: View {
    @EnvironmentObject var monitor: StreamMonitor

    var body: some View {
        HStack {
            if let url = Bundle.main.url(forResource: "StreamDVRIcon", withExtension: "png"),
               let icon = NSImage(contentsOf: url) {
                Image(nsImage: icon)
                    .resizable()
                    .interpolation(.high)
                    .frame(width: 34, height: 34)
            } else {
                Image(systemName: "video.fill")
                    .font(.system(size: 24))
                    .foregroundColor(.purple)
            }

            VStack(alignment: .leading, spacing: 2) {
                Text("StreamDVR")
                    .font(.title2.bold())
                Text(monitor.isMonitoring ? "Monitoring channels..." : "Waiting for start")
                    .font(.caption)
                    .foregroundColor(monitor.isMonitoring ? .green : .secondary)
            }

            Spacer()

            HStack(spacing: 8) {
                if monitor.isKickLoggedIn {
                    HStack(spacing: 6) {
                        Image(systemName: "checkmark.seal.fill")
                            .foregroundColor(.green)
                        Text(monitor.kickUsername.isEmpty ? "Kick" : "Kick: \(monitor.kickUsername)")
                            .font(.caption)
                        Button("Logout") {
                            monitor.logoutKick()
                        }
                        .font(.caption)
                    }
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(Color.green.opacity(0.15))
                    .cornerRadius(6)
                } else {
                    Button {
                        monitor.openKickLogin()
                    } label: {
                        Label("Login with KICK", systemImage: "person.crop.circle.badge.plus")
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.green)
                }

                if !monitor.isLoggedIn {
                    Button {
                        monitor.openLoginView()
                    } label: {
                        Label("Login with Twitch", systemImage: "person.crop.circle.badge.plus")
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.purple)
                } else {
                    HStack(spacing: 8) {
                        Image(systemName: "checkmark.seal.fill")
                            .foregroundColor(.green)
                        Text("Twitch")
                            .font(.caption)
                        Button("Logout") {
                            monitor.logout()
                        }
                        .font(.caption)
                    }
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(Color.green.opacity(0.15))
                    .cornerRadius(6)
                }
            }

            Button {
                if monitor.isMonitoring {
                    monitor.stopMonitoring()
                } else {
                    monitor.startMonitoring()
                }
            } label: {
                Label(monitor.isMonitoring ? "Stop Monitoring" : "Start Monitoring", systemImage: monitor.isMonitoring ? "stop.circle.fill" : "record.circle")
            }
            .buttonStyle(.borderedProminent)
            .tint(monitor.isMonitoring ? .red : .green)
            .disabled(monitor.channels.isEmpty)
        }
        .padding(12)
    }
}

struct ChannelListView: View {
    @EnvironmentObject var monitor: StreamMonitor
    @Binding var newChannelInput: String
    @State private var draggedChannelID: String?
    @State private var addPlatform: StreamPlatform = .twitch

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Picker("Platform", selection: $addPlatform) {
                    Text("Twitch (default)").tag(StreamPlatform.twitch)
                    Text("Chaturbate").tag(StreamPlatform.chaturbate)
                    Text("Kick").tag(StreamPlatform.kick)
                }
                .pickerStyle(.menu)
                .labelsHidden()
                .frame(width: 96)
                .help("Default platform for a plain channel name (no URL). Full URLs always win.")

                TextField("Channel name or URL (Twitch, Kick, Chaturbate)", text: $newChannelInput)
                    .textFieldStyle(.roundedBorder)
                    .frame(maxWidth: .infinity)
                    .onSubmit(addChannel)

                Button(action: addChannel) {
                    Label("Add Channel", systemImage: "plus.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(newChannelInput.trimmingCharacters(in: .whitespaces).isEmpty)

                if monitor.hasActiveRecordings {
                    Button {
                        monitor.stopAllRecordings()
                    } label: {
                        Label("Stop All", systemImage: "stop.fill")
                    }
                    .buttonStyle(.bordered)
                    .tint(.red)
                    .help("Stop all active recordings")
                }
            }

            if monitor.channels.isEmpty {
                VStack(spacing: 16) {
                    Spacer()
                    Image(systemName: "tv")
                        .font(.system(size: 48))
                        .foregroundColor(.gray.opacity(0.5))
                    Text("No channels added yet")
                        .font(.headline)
                        .foregroundColor(.secondary)
                    Text("Add a Twitch channel above to start automatic recording when it goes live.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 60)
                    Spacer()
                }
                .frame(maxWidth: .infinity)
            } else {
                ScrollView {
                    LazyVStack(spacing: 0) {
                        ForEach(monitor.sortedChannels) { channel in
                            HStack(spacing: 6) {
                                Image(systemName: "line.3.horizontal")
                                    .font(.system(size: 14))
                                    .foregroundColor(.secondary)
                                    .contentShape(Rectangle())
                                    .frame(width: 18, height: 24)
                                    .help("Drag to reorder")
                                    .onDrag {
                                        draggedChannelID = channel.id
                                        return NSItemProvider(object: "\(channel.id)" as NSString)
                                    }

                                ChannelRowView(channel: channel)
                            }
                            .padding(.vertical, 4)
                            .padding(.horizontal, 12)
                            .onDrop(of: [.text], delegate: ReorderDropDelegate(
                                targetID: channel.id,
                                monitor: monitor,
                                draggedID: $draggedChannelID
                            ))

                            Divider()
                        }
                    }
                }
            }
        }
        .padding(12)
    }

    private func addChannel() {
        let input = newChannelInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !input.isEmpty else { return }
        monitor.addChannel(input: input, defaultPlatform: addPlatform)
        newChannelInput = ""
    }
}

struct ReorderDropDelegate: DropDelegate {
    let targetID: String
    let monitor: StreamMonitor
    @Binding var draggedID: String?

    func dropEntered(info: DropInfo) {
        guard let fromID = draggedID,
              fromID != targetID,
              let from = monitor.channels.firstIndex(where: { $0.id == fromID }),
              let to = monitor.channels.firstIndex(where: { $0.id == targetID })
        else { return }
        withAnimation {
            monitor.channels.move(fromOffsets: IndexSet(integer: from), toOffset: to > from ? to + 1 : to)
        }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        DropProposal(operation: .move)
    }

    func performDrop(info: DropInfo) -> Bool {
        draggedID = nil
        monitor.saveChannelOrder()
        return true
    }
}

struct ChannelRowView: View {
    @EnvironmentObject var monitor: StreamMonitor
    let channel: StreamChannel

    // Resolve the current, live copy of this channel from the published array.
    // This makes the row re-read refreshed data (title/game) whenever the
    // @Published `channels` changes — otherwise SwiftUI's List keeps showing
    // the stale captured value until a forced re-render.
    private var live: StreamChannel {
        monitor.channels.first(where: { $0.id == channel.id }) ?? channel
    }

    var body: some View {
        let current = live
        let status = monitor.recordingStatuses[current.id] ?? .idle

        HStack(spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Color.purple.opacity(0.2))
                    .frame(width: 44, height: 44)
                if !current.profileImageURL.isEmpty, let url = URL(string: current.profileImageURL) {
                    AsyncImage(url: url) { phase in
                        if let image = phase.image {
                            image
                                .resizable()
                                .scaledToFill()
                        } else {
                            initialView
                        }
                    }
                    .frame(width: 44, height: 44)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                } else {
                    initialView
                }
            }

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(current.displayName)
                        .font(.headline)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            openChannel(current)
                        }
                        .help("Open \(current.platform.displayName)/\(current.login) in browser")
                    Group {
                        switch current.platform {
                        case .twitch:
                            Text("Twitch")
                                .foregroundColor(.purple)
                                .background(Color.purple.opacity(0.12))
                        case .chaturbate:
                            Text("Chaturbate")
                                .foregroundColor(.pink)
                                .background(Color.pink.opacity(0.15))
                        case .kick:
                            Text("Kick")
                                .foregroundColor(.green)
                                .background(Color.green.opacity(0.15))
                        }
                    }
                    .font(.caption2.bold())
                    .padding(.horizontal, 6)
                    .padding(.vertical, 1)
                    .clipShape(Capsule())
                    Circle()
                            .fill(!current.currentStreamTitle.isEmpty ? Color.green : Color.gray.opacity(0.6))
                            .frame(width: 8, height: 8)
                    if current.isIgnored {
                        Text("Ignored")
                            .font(.caption2.bold())
                            .foregroundColor(.red)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 1)
                            .background(Color.red.opacity(0.12))
                            .clipShape(Capsule())
                    }
                }
                if !current.currentStreamTitle.isEmpty {
                    Text(current.currentStreamTitle)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                } else if current.isIgnored {
                    Text("Ignored — not monitored")
                        .font(.caption)
                        .foregroundColor(.secondary)
                } else {
                    Text("Offline")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                if !current.currentGame.isEmpty {
                    Text(current.currentGame)
                        .font(.caption2)
                        .foregroundColor(.purple.opacity(0.7))
                }
            }

            Spacer()

            VStack(alignment: .trailing, spacing: 2) {
                Text(status.label)
                    .font(.subheadline)
                    .foregroundColor(status.isActive ? .red : .secondary)
                if status.isActive, let info = monitor.recordingInfo[current.id] {
                    Text(statsLine(info))
                        .font(.caption2)
                        .foregroundColor(.secondary)
                        .monospacedDigit()
                }
            }

            if status.isActive {
                Circle()
                    .fill(Color.red)
                    .frame(width: 10, height: 10)
                    .shadow(color: .red, radius: 4)
            }

            HStack(spacing: 4) {
                Button {
                    if status.isActive {
                        monitor.stopRecording(current)
                    } else {
                        monitor.startRecording(current)
                    }
                } label: {
                    Image(systemName: status.isActive ? "stop.fill" : "record.circle")
                        .frame(width: 20, height: 20)
                }
                .buttonStyle(.borderless)
                .help(status.isActive ? "Stop recording" : "Record now")

                Button {
                    monitor.openRecordingFolder(for: current)
                } label: {
                    Image(systemName: "folder")
                        .frame(width: 20, height: 20)
                }
                .buttonStyle(.borderless)
                .help("Open recording folder")

                Button {
                    monitor.setIgnored(current, !current.isIgnored)
                } label: {
                    Image(systemName: "circle.slash")
                        .frame(width: 20, height: 20)
                }
                .buttonStyle(.borderless)
                .foregroundColor(current.isIgnored ? .red : .secondary)
                .help(current.isIgnored ? "Stop ignoring channel" : "Ignore channel (exclude from monitoring)")

                Button {
                    monitor.removeChannel(current)
                } label: {
                    Image(systemName: "trash")
                        .frame(width: 20, height: 20)
                }
                .buttonStyle(.borderless)
                .foregroundColor(.red)
                .help("Remove channel")
            }
        }
        .padding(.vertical, 2)
    }

    private func openChannel(_ channel: StreamChannel) {
        if let url = URL(string: monitor.watchURL(for: channel)) {
            NSWorkspace.shared.open(url)
        }
    }

    private var initialView: some View {
        Text(String(live.displayName.prefix(1)).uppercased())
            .font(.headline)
            .foregroundColor(.purple)
    }

    private func statsLine(_ info: RecordingStats) -> String {
        var parts: [String] = []
        parts.append(formattedSize(info.fileSize))
        if !info.resolution.isEmpty { parts.append(info.resolution) }
        if !info.bitrate.isEmpty { parts.append(info.bitrate) }
        return parts.joined(separator: " · ")
    }

    private func formattedSize(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }
}

struct RecordingsView: View {
    @EnvironmentObject var monitor: StreamMonitor
    @State private var files: [URL] = []
    @State private var directoryExists = true
    @State private var isLoading = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("Recordings Directory")
                    .font(.headline)
                Spacer()
                Text(monitor.outputDirectory)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .textSelection(.enabled)
                Button {
                    let url = URL(fileURLWithPath: monitor.outputDirectory)
                    NSWorkspace.shared.open(url)
                } label: {
                    Image(systemName: "folder")
                }
                .buttonStyle(.borderless)
                .help("Open recordings folder")
                Button {
                    reloadFiles()
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .buttonStyle(.borderless)
                .help("Refresh recordings list")
            }
            .padding(.horizontal, 12)
            .padding(.top, 12)

            if isLoading {
                Spacer()
                ProgressView("Loading recordings...")
                    .frame(maxWidth: .infinity)
                Spacer()
            } else if !directoryExists {
                Spacer()
                Text("Directory does not exist")
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity)
                Spacer()
            } else if files.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "folder")
                        .font(.system(size: 40))
                        .foregroundColor(.gray)
                    Text("No recordings yet")
                        .foregroundColor(.secondary)
                }
                .frame(maxWidth: .infinity)
                Spacer()
            } else {
                List(files, id: \.self) { file in
                    HStack {
                        Image(systemName: file.pathExtension == "mp4" ? "film" : "waveform")
                            .foregroundColor(.purple)
                        Text(relativePath(file))
                            .lineLimit(1)
                        Spacer()
                        Text(formattedSize(file: file))
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Button {
                            NSWorkspace.shared.activateFileViewerSelecting([file])
                        } label: {
                            Image(systemName: "magnifyingglass")
                        }
                        .buttonStyle(.borderless)
                    }
                }
            }
        }
        .onAppear {
            reloadFiles()
        }
        .onChange(of: monitor.outputDirectory) { _ in
            reloadFiles()
        }
    }

    private func reloadFiles() {
        let dir = monitor.outputDirectory
        DispatchQueue.global(qos: .userInitiated).async {
            let exists = FileManager.default.fileExists(atPath: dir)
            let list = Self.buildFileList(dir)
            DispatchQueue.main.async {
                self.directoryExists = exists
                self.files = list
                self.isLoading = false
            }
        }
        isLoading = true
    }

    private static func buildFileList(_ dir: String) -> [URL] {
        let url = URL(fileURLWithPath: dir)
        var result: [URL] = []
        guard let enumerator = FileManager.default.enumerator(
            at: url,
            includingPropertiesForKeys: [.creationDateKey],
            options: [.skipsHiddenFiles]) else { return result }

        for case let fileURL as URL in enumerator {
            if fileURL.pathExtension == "ts" || fileURL.pathExtension == "mp4" {
                result.append(fileURL)
            }
        }

        return result.sorted {
            (try? $0.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? .distantPast
                > (try? $1.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? .distantPast
        }
    }

    private func relativePath(_ url: URL) -> String {
        let base = monitor.outputDirectory.hasSuffix("/") ? monitor.outputDirectory : monitor.outputDirectory + "/"
        let path = url.path.hasPrefix(base) ? String(url.path.dropFirst(base.count)) : url.lastPathComponent
        return path
    }

    private func formattedSize(file: URL) -> String {
        let attrs = try? FileManager.default.attributesOfItem(atPath: file.path)
        let size = attrs?[.size] as? Int64 ?? 0
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: size)
    }
}

struct StatusBarView: View {
    @EnvironmentObject var monitor: StreamMonitor

    private var onlineCount: Int {
        monitor.channels.filter { !$0.currentStreamTitle.isEmpty }.count
    }

    private var recordingCount: Int {
        monitor.recordingStatuses.values.filter(\.isActive).count
    }

    private var ignoredCount: Int {
        monitor.channels.filter(\.isIgnored).count
    }

    private var appVersion: String {
        Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? ""
    }

    var body: some View {
        HStack(spacing: 16) {
            Label("\(monitor.channels.count) channels", systemImage: "list.bullet")
                .foregroundColor(.secondary)
            Label("\(onlineCount) live", systemImage: "antenna.radiowaves.left.and.right")
                .foregroundColor(.green)
            Label("\(monitor.channels.count - onlineCount) offline", systemImage: "moon")
                .foregroundColor(.secondary)
            Label("\(ignoredCount) ignored", systemImage: "circle.slash")
                .foregroundColor(ignoredCount > 0 ? .red : .secondary)
            Label("\(recordingCount) recording", systemImage: "record.circle")
                .foregroundColor(recordingCount > 0 ? .red : .secondary)

            Spacer()

            switch monitor.updateState {
            case .updateAvailable(let info):
                Button {
                    monitor.downloadAndInstallUpdate()
                } label: {
                    Label("Update \(info.version) available", systemImage: "arrow.down.circle.fill")
                        .foregroundColor(.blue)
                }
                .buttonStyle(.plain)
                .help("Download and install the new version")
            case .downloading(let info):
                HStack(spacing: 5) {
                    ProgressView().controlSize(.small)
                    Text("Downloading \(info.version)...")
                        .foregroundColor(.secondary)
                }
            default:
                EmptyView()
            }

            if monitor.installingDependencies {
                HStack(spacing: 5) {
                    ProgressView().controlSize(.small)
                    Text("Installing dependencies…")
                        .foregroundColor(.secondary)
                }
            }

            if !appVersion.isEmpty {
                Label("Version \(appVersion)", systemImage: "checkmark.seal.fill")
                    .foregroundColor(.green)
            }
        }
        .font(.caption)
        .padding(.horizontal, 12)
        .padding(.vertical, 5)
        .background(Color(NSColor.windowBackgroundColor).opacity(0.9))
    }
}

struct LogsView: View {
    @EnvironmentObject var monitor: StreamMonitor

    var body: some View {
        VStack(alignment: .leading) {
            HStack {
                Text("Activity Log")
                    .font(.headline)
                Spacer()
                Button("Clear") {
                    monitor.logs.removeAll()
                }
                .buttonStyle(.borderless)
            }
            .padding(.horizontal, 12)
            .padding(.top, 12)

            ScrollView {
                LazyVStack(alignment: .leading, spacing: 4) {
                    ForEach(monitor.logs) { log in
                        HStack(alignment: .top, spacing: 8) {
                            Text(log.icon)
                            Text(timestampString(log.timestamp))
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Text(log.message)
                                .font(.callout)
                                .textSelection(.enabled)
                            Spacer()
                        }
                        .padding(.horizontal, 12)
                    }
                }
                .padding(.vertical, 8)
            }
            .background(Color(NSColor.textBackgroundColor))
            .padding(12)
        }
    }

    private func timestampString(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "HH:mm:ss"
        return formatter.string(from: date)
    }
}

struct SettingsView: View {
    @EnvironmentObject var monitor: StreamMonitor

    var body: some View {
        Form {
            Section("Account (Twitch)") {
                if monitor.isLoggedIn {
                    Label("Logged in as \(monitor.loggedInUsername.isEmpty ? "Twitch user" : monitor.loggedInUsername)", systemImage: "checkmark.seal.fill")
                        .foregroundColor(.green)
                    Button("Log out of Twitch") {
                        monitor.openLoginView()
                    }
                } else {
                    Text("Sign in to Twitch to record streams. Premium / Prime users get ad-free recordings.")
                        .font(.caption)
                    Button("Sign in to Twitch") {
                        monitor.openLoginView()
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.purple)
                }
                Text("Logging in opens Twitch's official page inside the app — you only enter your login and password there. No tokens to paste.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            Section("Account (Kick)") {
                if monitor.isKickLoggedIn {
                    Label(monitor.kickUsername.isEmpty ? "Kick session active" : "Logged in as \(monitor.kickUsername)", systemImage: "checkmark.seal.fill")
                        .foregroundColor(.green)
                    Button("Log out of Kick") {
                        monitor.logoutKick()
                    }
                } else {
                    Text("Optional — Kick streams record without logging in. Signing in sends your session cookies to Kick (e.g. ad behavior for your account).")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Button("Sign in to Kick") {
                        monitor.openKickLogin()
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.green)
                }
            }

            Section("Updates") {
                Toggle("Check for updates on launch", isOn: $monitor.checkUpdates)
                    .onChange(of: monitor.checkUpdates) { _ in
                        monitor.saveUpdatePref()
                    }
                HStack(spacing: 8) {
                    Button("Check now") {
                        monitor.checkForUpdate()
                    }
                    updateStatusView
                }
            }

            Section("Channel List") {
                Toggle("Auto sort — live channels to the top", isOn: $monitor.autoSortLive)
                    .onChange(of: monitor.autoSortLive) { _ in
                        monitor.saveAutoSortLive()
                    }
                Text("Channels currently live move to the top of the list so they are easy to spot. Your manual order is kept and restored when this is off.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            Section("Backup & Restore") {
                HStack(spacing: 8) {
                    Button("Create Backup...") {
                        exportBackup()
                    }
                    Button("Restore from Backup...") {
                        importBackup()
                    }
                }
                Text("Saves your settings (account tokens, output folder, monitoring options) and the channel list to a JSON file — handy for reinstalls or moving to another Mac.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            Section("Notifications") {
                Toggle("Notify when a channel goes live", isOn: $monitor.liveNotifications)
                    .onChange(of: monitor.liveNotifications) { _ in
                        monitor.saveLiveNotifications()
                    }
                Text("Shows a banner (with sound) the moment one of your channels starts streaming. Permission is requested the first time you enable this — manage it later in System Settings > Notifications.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            Section("Status Check") {
                Picker("Check for live streams every", selection: $monitor.pollInterval) {
                    Text("15 seconds").tag(TimeInterval(15))
                    Text("30 seconds").tag(TimeInterval(30))
                    Text("60 seconds").tag(TimeInterval(60))
                    Text("120 seconds").tag(TimeInterval(120))
                }
                .onChange(of: monitor.pollInterval) { _ in
                    monitor.savePollInterval()
                }
                Text("How often the app checks your channels for a live stream. Lower values detect streams faster but make more requests.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            Section("Recording Settings") {
                Toggle("Keep Mac awake while monitoring", isOn: $monitor.preventSleep)
                    .onChange(of: monitor.preventSleep) { _ in
                        monitor.savePreventSleep()
                    }
                Text("Prevents the Mac from idle-sleeping during monitoring (recordings keep running). Uses a native macOS power assertion — no extra permissions needed.")
                    .font(.caption2)
                    .foregroundColor(.secondary)

                Picker("Output Directory", selection: $monitor.outputDirectory) {
                    Text(monitor.outputDirectory).tag(monitor.outputDirectory)
                }
                .labelsHidden()

                HStack(spacing: 8) {
                    Button("Choose Folder...") {
                        chooseFolder()
                    }

                    Button("Open Folder") {
                        let url = URL(fileURLWithPath: monitor.outputDirectory)
                        NSWorkspace.shared.open(url)
                    }
                    .help("Open the recordings folder in Finder")
                }
            }
        }
        .formStyle(.grouped)
        .padding()
    }

    private func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Select"
        panel.message = "Choose where to save recordings"
        panel.directoryURL = URL(fileURLWithPath: monitor.outputDirectory)

        if panel.runModal() == .OK, let url = panel.url {
            monitor.outputDirectory = url.path
            ConfigStore.save(key: "twitch_output_dir", value: url.path)
        }
    }

    private func exportBackup() {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.nameFieldStringValue = "StreamDVR-backup-\(backupDateStamp).json"
        panel.message = "Save settings and channel list to a backup file"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        _ = monitor.exportBackup(to: url)
    }

    private func importBackup() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        panel.message = "Choose a StreamDVR backup file to restore"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        _ = monitor.importBackup(from: url)
    }

    private var backupDateStamp: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.string(from: Date())
    }

    @ViewBuilder private var updateStatusView: some View {
        switch monitor.updateState {
        case .idle:
            EmptyView()
        case .checking:
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Checking...").font(.caption).foregroundColor(.secondary)
            }
        case .upToDate(_, let latest):
            Label("Up to date (v\(latest))", systemImage: "checkmark.seal.fill")
                .font(.caption)
                .foregroundColor(.green)
        case .updateAvailable(let info):
            VStack(alignment: .leading, spacing: 6) {
                Label("Update \(info.version) available", systemImage: "arrow.down.circle")
                    .font(.caption)
                    .foregroundColor(.blue)
                Button("Download & Restart") {
                    monitor.downloadAndInstallUpdate()
                }
                .buttonStyle(.borderedProminent)
                .tint(.blue)
                .controlSize(.small)
            }
        case .downloading(let info):
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Downloading v\(info.version)...").font(.caption).foregroundColor(.secondary)
            }
        case .error(let message):
            Label(message, systemImage: "exclamationmark.triangle")
                .font(.caption)
                .foregroundColor(.red)
        }
    }
}
