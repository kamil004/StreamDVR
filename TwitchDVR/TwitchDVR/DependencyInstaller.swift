import Foundation

/// Checks for required CLI tools (streamlink, ffprobe) and installs them via
/// Homebrew when they are missing. Homebrew itself is installed on demand too.
/// Non-interactive — suitable for app-launch.
enum DependencyInstaller {

    enum LogLevel { case info, warning, error, success }

    // MARK: - Public

    static func ensureAll(log: @escaping (String, LogLevel) -> Void) {
        ensureStreamlink(log: log)
        ensureFfprobe(log: log)
    }

    // MARK: - streamlink

    private static func ensureStreamlink(log: @escaping (String, LogLevel) -> Void) {
        guard findStreamlink() == nil else { return }

        guard ensureHomebrew(log: log) else {
            log("Missing component: Homebrew (required for streamlink). Install it manually from https://brew.sh, then relaunch the app.", .error)
            return
        }

        log("streamlink not found — installing via Homebrew…", .info)
        let exit = runBrew(args: ["install", "streamlink"])
        if exit == 0 {
            log("streamlink installed successfully", .success)
        } else {
            log("Missing component: streamlink. Installation failed (exit \(exit)) — run `brew install streamlink` in Terminal, then relaunch the app.", .error)
        }
    }

    // MARK: - ffprobe

    private static func ensureFfprobe(log: @escaping (String, LogLevel) -> Void) {
        guard findFfprobe() == nil else { return }

        guard ensureHomebrew(log: log) else {
            log("Missing component: Homebrew (required for ffprobe). Install it manually from https://brew.sh, then relaunch the app.", .error)
            return
        }

        log("ffprobe not found — installing ffmpeg via Homebrew…", .info)
        let exit = runBrew(args: ["install", "ffmpeg"])
        if exit == 0 {
            log("ffmpeg (ffprobe) installed successfully", .success)
        } else {
            log("Missing component: ffprobe. Installation failed (exit \(exit)) — run `brew install ffmpeg` in Terminal, then relaunch the app.", .error)
        }
    }

    // MARK: - Homebrew

    /// Ensures Homebrew is available, installing it if missing.
    /// Returns true when `brew` exists afterwards.
    private static func ensureHomebrew(log: @escaping (String, LogLevel) -> Void) -> Bool {
        if findBrew() != nil { return true }

        log("Homebrew not found — installing Homebrew…", .info)
        let installScript = "/bin/bash -c \"$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        let exit = runShell(installScript, env: ["NONINTERACTIVE": "1"])
        if exit == 0 {
            log("Homebrew installed successfully", .success)
            return true
        }

        // A non-interactive install commonly fails because sudo is required
        // (no admin elevation possible from a background process).
        log("Homebrew installation failed (exit \(exit))", .error)
        log("Install Homebrew manually from https://brew.sh — administrator password may be required.", .warning)
        return false
    }

    // MARK: - Finders

    static func findStreamlink() -> String? {
        let known = [
            "/opt/homebrew/bin/streamlink",
            "/usr/local/bin/streamlink",
            "/usr/bin/streamlink",
            "/opt/local/bin/streamlink"
        ]
        for p in known { if FileManager.default.isExecutableFile(atPath: p) { return p } }
        return findOnPath("streamlink")
    }

    static func findFfprobe() -> String? {
        let known = [
            "/opt/homebrew/bin/ffprobe",
            "/usr/local/bin/ffprobe",
            "/usr/bin/ffprobe"
        ]
        for p in known { if FileManager.default.isExecutableFile(atPath: p) { return p } }
        return findOnPath("ffprobe")
    }

    private static func findBrew() -> String? {
        let known = ["/opt/homebrew/bin/brew", "/usr/local/bin/brew"]
        for p in known { if FileManager.default.isExecutableFile(atPath: p) { return p } }
        return findOnPath("brew")
    }

    private static func findOnPath(_ name: String) -> String? {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/bin/zsh")
        proc.arguments = ["-c", "command -v \(name) 2>/dev/null"]
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        try? proc.run()
        proc.waitUntilExit()
        let out = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard let out = out, !out.isEmpty, FileManager.default.isExecutableFile(atPath: out) else { return nil }
        return out
    }

    // MARK: - Run helpers

    private static func runBrew(args: [String]) -> Int32 {
        guard let brew = findBrew() else { return -1 }
        return runExecutable(url: URL(fileURLWithPath: brew), args: args, env: nil)
    }

    private static func runShell(_ command: String, env: [String: String]) -> Int32 {
        runExecutable(url: URL(fileURLWithPath: "/bin/bash"), args: ["-c", command], env: env)
    }

    private static func runExecutable(url: URL, args: [String], env: [String: String]?) -> Int32 {
        let proc = Process()
        proc.executableURL = url
        proc.arguments = args
        if let env = env {
            var merged = ProcessInfo.processInfo.environment
            for (k, v) in env { merged[k] = v }
            proc.environment = merged
        }
        let devNull = Pipe()
        proc.standardOutput = devNull
        proc.standardError = devNull
        try? proc.run()
        proc.waitUntilExit()
        return proc.terminationStatus
    }
}