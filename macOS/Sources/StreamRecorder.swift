import Foundation

class StreamRecorder {
    private var process: Process?
    private var completionHandler: ((String) -> Void)?
    private var isRunning = false
    private var fileHandle: FileHandle?
    private(set) var outputPath: String?

    func startRecording(url: String, outputDir: String, channelName: String, accessToken: String?, platform: StreamPlatform, streamInfo: StreamInfo, completion: @escaping (String) -> Void) {
        self.completionHandler = completion
        let streamlinkPath = findStreamlink()
        guard let path = streamlinkPath else {
            completion("Error: streamlink not found. Install with: brew install streamlink")
            return
        }

        let now = Date()
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        let dateString = formatter.string(from: now)

        formatter.dateFormat = "HH-mm-ss"
        let timeString = formatter.string(from: now)

        let safeChannel = Self.sanitizeFilename(channelName)
        let finalChannel = safeChannel.isEmpty ? "channel" : safeChannel

        let safeTitle = Self.sanitizeFilename(streamInfo.title)
        let finalTitle = safeTitle.isEmpty ? "stream" : String(safeTitle.prefix(35))

        let filename = "\(finalChannel)_\(dateString)-\(timeString)_\(finalTitle).ts"
        let outputPath = (outputDir as NSString).appendingPathComponent(filename)
        try? FileManager.default.createDirectory(atPath: outputDir, withIntermediateDirectories: true)
        self.outputPath = outputPath

        var args: [String] = []

        // Twitch-only flags: ad removal + premium ad-free auth via the web token.
        if platform == .twitch {
            args.append(contentsOf: ["--twitch-disable-ads"])

            // The web auth-token (from the login cookie) authenticates Twitch API calls
            // so premium / ad-free users get a clean stream. Sent as an API header.
            // Note: streamlink expects KEY=VALUE format; streamlink 8.x no longer has --twitch-client-id.
            if let token = accessToken, !token.isEmpty {
                args.append(contentsOf: ["--twitch-api-header", "Authorization=OAuth \(token)"])
            }
        }

        // Kick: optional session cookies are forwarded to streamlink so requests
        // are made as the signed-in user (recording still works without them).
        if platform == .kick {
            for (name, value) in KickSession.cookies {
                args.append(contentsOf: ["--http-cookie", "\(name)=\(value)"])
            }
        }

        args.append(contentsOf: [
            "-o", outputPath,
            "--force",
            "--stream-segment-attempts", "10",
            "--stream-segment-timeout", "30",
            "--retry-open", "30",
            "--retry-streams", "10",
            "--retry-max", "10",
            "--ringbuffer-size", "32M"
        ])

        args.append(url)
        args.append("best")

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: path)
        proc.arguments = args

        proc.environment = Self.augmentedEnvironment()

        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = pipe

        pipe.fileHandleForReading.readabilityHandler = { handle in
            // Drain stdout/stderr so streamlink never blocks on a full pipe.
            // Errors surface via the process exit status and finalization log.
            _ = handle.availableData
        }

        proc.terminationHandler = { [weak self] proc in
            DispatchQueue.main.async {
                self?.isRunning = false
                pipe.fileHandleForReading.readabilityHandler = nil
                if proc.terminationStatus == 0 || proc.terminationStatus == 1 {
                    self?.complete(outputPath)
                } else {
                    self?.complete("Recording stopped (exit code \(proc.terminationStatus))")
                }
            }
        }

        do {
            try proc.run()
            self.process = proc
            self.isRunning = true
        } catch {
            completion("Failed to start streamlink: \(error.localizedDescription)")
        }
    }

    func stop() {
        if let proc = process, proc.isRunning {
            proc.terminate()
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak proc] in
                if let p = proc, p.isRunning {
                    p.interrupt()
                }
            }
        }
        isRunning = false
    }

    /// Finalizes the recording: if the output file was created but no real data
    /// ever arrived (some streams, e.g. very low bitrate starts, produce an
    /// empty/header-only .ts), it is discarded instead of being left behind.
    /// Otherwise the file's base PTS is normalized to ~0 so players start at
    /// 00:00 (some sources keep a large base timestamp, e.g. ~1h44m).
    private func complete(_ output: String) {
        guard let completion = completionHandler else { return }
        let stoppedOrFailed = output.hasPrefix("Recording stopped") || output.hasPrefix("Error")
        guard let path = self.outputPath else {
            completion(output)
            return
        }
        if stoppedOrFailed, !FileManager.default.fileExists(atPath: path) {
            completion(output)
            return
        }
        let size = (try? FileManager.default.attributesOfItem(atPath: path)[.size] as? Int64) ?? -1
        if FileManager.default.fileExists(atPath: path), size < 1024 {
            try? FileManager.default.removeItem(atPath: path)
            completion("discarded:\(path)")
            return
        }
        DispatchQueue.global(qos: .utility).async {
            let result = Self.finalizeToMp4(path, fallbackOutput: output)
            DispatchQueue.main.async {
                completion(result)
            }
        }
    }

    /// Converts a finished recording to MP4 with stream copy (fast, no
    /// re-encoding): MP4 rebases timestamps so every player starts at 00:00.
    /// Falls back to re-encoding only the audio if the copy is refused
    /// (e.g. opus audio on LL-HLS), and keeps the original .ts on failure.
    private static func finalizeToMp4(_ path: String, fallbackOutput: String) -> String {
        guard path.hasSuffix(".ts") else { return fallbackOutput }
        let mp4 = String(path.dropLast(3)) + ".mp4"

        Self.runProcess("ffmpeg", ["-y", "-v", "error", "-i", path, "-map", "0", "-c", "copy", "-movflags", "+faststart", mp4])
        if FileManager.default.fileExists(atPath: mp4),
           let size = try? FileManager.default.attributesOfItem(atPath: mp4)[.size] as? Int64,
           size > 0 {
            try? FileManager.default.removeItem(atPath: path)
            return "converted:\(mp4)"
        }
        try? FileManager.default.removeItem(atPath: mp4)

        // Fallback: transcode audio only (video stays copied).
        Self.runProcess("ffmpeg", ["-y", "-v", "error", "-i", path, "-map", "0", "-c:v", "copy", "-c:a", "aac", "-movflags", "+faststart", mp4])
        if FileManager.default.fileExists(atPath: mp4),
           let size = try? FileManager.default.attributesOfItem(atPath: mp4)[.size] as? Int64,
           size > 0 {
            try? FileManager.default.removeItem(atPath: path)
            return "converted:\(mp4)"
        }
        try? FileManager.default.removeItem(atPath: mp4)
        return "convertfailed:\(path)"
    }

    /// Runs a tool found on PATH (ffprobe/ffmpeg from the Homebrew tool dirs)
    /// and returns its captured stdout, or nil on failure.
    @discardableResult
    private static func runProcess(_ tool: String, _ arguments: [String]) -> String? {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        proc.arguments = [tool] + arguments
        proc.environment = augmentedEnvironment()
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        do {
            try proc.run()
        } catch {
            return nil
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        proc.waitUntilExit()
        return String(data: data, encoding: .utf8)
    }

    /// GUI-launched apps have a minimal PATH without the Homebrew tool dirs.
    /// streamlink needs ffmpeg on PATH to use its ffmpeg muxer; without it,
    /// Chaturbate (LL-HLS/fMP4) recordings lose audio and keep the source
    /// stream's large base PTS (a long blank start). Expose the tool dirs.
    private static func augmentedEnvironment() -> [String: String] {
        var environment = ProcessInfo.processInfo.environment
        let toolBins = ["/opt/homebrew/bin", "/usr/local/bin"]
        if let currentPath = environment["PATH"] {
            environment["PATH"] = (toolBins + [currentPath]).joined(separator: ":")
        } else {
            environment["PATH"] = (toolBins + ["/usr/bin", "/bin", "/usr/sbin", "/sbin"]).joined(separator: ":")
        }
        return environment
    }

    static func sanitizeFilename(_ input: String) -> String {
        input
            .replacingOccurrences(of: "/", with: "-")
            .replacingOccurrences(of: ":", with: "-")
            .replacingOccurrences(of: "\\", with: "-")
            .replacingOccurrences(of: "\"", with: "")
            .replacingOccurrences(of: "*", with: "")
            .replacingOccurrences(of: "?", with: "")
            .replacingOccurrences(of: "<", with: "")
            .replacingOccurrences(of: ">", with: "")
            .replacingOccurrences(of: "|", with: "-")
            .trimmingCharacters(in: .whitespaces)
    }

    private func findStreamlink() -> String? {
        let paths = [
            "/opt/homebrew/bin/streamlink",
            "/usr/local/bin/streamlink",
            "/usr/bin/streamlink",
            "/opt/local/bin/streamlink",
            "/usr/sbin/streamlink"
        ]

        for path in paths {
            if FileManager.default.fileExists(atPath: path) {
                return path
            }
        }

        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/bash")
        task.arguments = ["-c", "which streamlink 2>/dev/null || echo ''"]
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = FileHandle.nullDevice
        try? task.run()
        task.waitUntilExit()

        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let path = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines)
        if let path = path, !path.isEmpty, FileManager.default.fileExists(atPath: path) {
            return path
        }

        return nil
    }
}
