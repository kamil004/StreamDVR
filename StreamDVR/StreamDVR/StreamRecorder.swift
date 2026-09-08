import Foundation

class StreamRecorder {
    private var process: Process?
    private var outputHandler: ((String) -> Void)?
    private var isRunning = false
    private var fileHandle: FileHandle?
    private(set) var outputPath: String?

    func startRecording(url: String, outputDir: String, accessToken: String?, platform: StreamPlatform, streamInfo: StreamInfo, completion: @escaping (String) -> Void) {
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

        let safeTitle = streamInfo.title
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
        let finalTitle = safeTitle.isEmpty ? "stream" : safeTitle

        let filename = "\(dateString)_\(finalTitle)_\(timeString).ts"
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

        // GUI-launched apps have a minimal PATH without the Homebrew tool dirs.
        // streamlink needs ffmpeg on PATH to use its ffmpeg muxer; without it,
        // Chaturbate (LL-HLS/fMP4) recordings lose audio and keep the source
        // stream's large base PTS (a long blank start). Expose the tool dirs.
        var environment = ProcessInfo.processInfo.environment
        let toolBins = ["/opt/homebrew/bin", "/usr/local/bin"]
        if let currentPath = environment["PATH"] {
            environment["PATH"] = (toolBins + [currentPath]).joined(separator: ":")
        } else {
            environment["PATH"] = (toolBins + ["/usr/bin", "/bin", "/usr/sbin", "/sbin"]).joined(separator: ":")
        }
        proc.environment = environment

        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = pipe

        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if let str = String(data: data, encoding: .utf8) {
                DispatchQueue.main.async {
                    if str.contains("[info]") || str.contains("[stream]") || str.contains("[cli]") {
                        // just ignore info lines
                    }
                    if str.contains("error") || str.contains("Error") {
                        self?.outputHandler?("Streamlink error: \(str.trimmingCharacters(in: .whitespacesAndNewlines))")
                    }
                }
            }
        }

        self.outputHandler = { output in
            // File was saved
            completion(output)
        }

        proc.terminationHandler = { [weak self] proc in
            DispatchQueue.main.async {
                self?.isRunning = false
                pipe.fileHandleForReading.readabilityHandler = nil
                if proc.terminationStatus == 0 || proc.terminationStatus == 1 {
                    completion(outputPath)
                } else {
                    completion("Recording stopped (exit code \(proc.terminationStatus))")
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
