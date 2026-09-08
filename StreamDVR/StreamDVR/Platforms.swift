import Foundation

// MARK: - Stream platform

enum StreamPlatform: String, Codable, CaseIterable, Identifiable {
    case twitch
    case chaturbate
    case kick

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .twitch: return "Twitch"
        case .chaturbate: return "Chaturbate"
        case .kick: return "Kick"
        }
    }
}

// MARK: - Channel model

struct StreamChannel: Identifiable, Codable, Hashable {
    let id: String
    var login: String
    var displayName: String
    var platform: StreamPlatform = .twitch
    var isRecording: Bool = false
    var isIgnored: Bool = false
    var currentStreamTitle: String = ""
    var currentGame: String = ""
    var profileImageURL: String = ""

    func hash(into hasher: inout Hasher) { hasher.combine(id) }
    static func == (lhs: StreamChannel, rhs: StreamChannel) -> Bool { lhs.id == rhs.id }
}

// MARK: - Status

struct ChannelStatus {
    let isLive: Bool
    let title: String
    let game: String
    let profileImageURL: String
}

// MARK: - Provider protocol

protocol ChannelProvider {
    /// Builds the canonical page URL to open the channel in a browser.
    var watchURL: (String) -> String { get }

    /// Live-playback URL consumed by streamlink (HLS m3u8). Falls back to
    /// `watchURL` for platforms handled by streamlink's own plugins.
    func playbackURL(login: String) async throws -> String?

    /// Resolves channel metadata (display name + avatar). `nil` if the channel doesn't exist.
    func resolveChannel(login: String) async throws -> StreamChannel?

    /// Channel status ALWAYS returned (even when offline), including the avatar.
    /// `nil` only when the channel/user doesn't exist.
    func getStatus(login: String) async throws -> ChannelStatus?

    /// Live-stream info used to name the recording, or `nil` when offline.
    func getStreamInfo(login: String) async throws -> StreamInfo?
}

// MARK: - Twitch

struct TwitchProvider: ChannelProvider {
    var watchURL: (String) -> String { { "https://www.twitch.tv/\($0)" } }

    func playbackURL(login: String) async throws -> String? {
        watchURL(login)
    }

    func resolveChannel(login: String) async throws -> StreamChannel? {
        guard let user = try await TwitchAPI.shared.getUser(login: login) else { return nil }
        return StreamChannel(
            id: "twitch:\(user.login)",
            login: user.login,
            displayName: user.displayName,
            platform: .twitch,
            profileImageURL: user.profileImageURL
        )
    }

    func getStatus(login: String) async throws -> ChannelStatus? {
        try await TwitchAPI.shared.getChannelStatus(login: login)
    }

    func getStreamInfo(login: String) async throws -> StreamInfo? {
        try await TwitchAPI.shared.getStream(login: login)
    }
}

// MARK: - Chaturbate

struct ChaturbateProvider: ChannelProvider {
    var watchURL: (String) -> String { { "https://chaturbate.com/\($0)" } }

    func playbackURL(login: String) async throws -> String? {
        let html = try await fetchHTML(login: login)
        return Self.extractM3U8(html)
    }

    func resolveChannel(login: String) async throws -> StreamChannel? {
        let html = try await fetchHTML(login: login)
        guard !html.isEmpty else { return nil }
        return StreamChannel(
            id: "chaturbate:\(login)",
            login: login,
            displayName: Self.parseDisplayName(html) ?? login,
            platform: .chaturbate,
            profileImageURL: Self.parseAvatar(html) ?? ""
        )
    }

    func getStatus(login: String) async throws -> ChannelStatus? {
        let html = try await fetchHTML(login: login)
        guard !html.isEmpty else { return nil }
        let live = Self.extractM3U8(html) != nil
        return ChannelStatus(
            isLive: live,
            title: live ? Self.parseTitle(html) : "",
            game: "",
            profileImageURL: Self.parseAvatar(html) ?? ""
        )
    }

    func getStreamInfo(login: String) async throws -> StreamInfo? {
        let html = try await fetchHTML(login: login)
        guard !html.isEmpty, let m3u8 = Self.extractM3U8(html) else { return nil }
        return StreamInfo(
            title: Self.parseTitle(html),
            game: "",
            viewerCount: 0,
            thumbnailURL: "",
            streamM3U8: m3u8,
            accessToken: nil,
            profileImageURL: Self.parseAvatar(html) ?? ""
        )
    }

    private func fetchHTML(login: String) async throws -> String {
        let url = URL(string: "https://chaturbate.com/\(login)/")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 20
        request.setValue(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
            forHTTPHeaderField: "User-Agent")
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw URLError(.badServerResponse)
        }
        // 404 => no such room; 403/other => transient, return empty to avoid treating as offline.
        guard http.statusCode == 200 else { return "" }
        return String(data: data, encoding: .utf8) ?? ""
    }

    /// Extracts the live room's HLS playlist URL and unescapes the embedded
    /// `\uXXXX` HTML/JS escapes Chaturbate puts in the page. `nil` when offline.
    private static func extractM3U8(_ html: String) -> String? {
        guard let raw = capture(#"https?://[^"'\s]+\.m3u8[^"'\s]*"#, in: html) else { return nil }
        var url = raw
        for sep in ["\\u0022", "\"", ","] {
            if let range = url.range(of: sep) {
                url = String(url[..<range.lowerBound])
            }
        }
        return decodeUnicodeEscapes(url)
    }

    private static func decodeUnicodeEscapes(_ text: String) -> String {
        guard let regex = try? NSRegularExpression(pattern: #"\\u([0-9a-fA-F]{4})"#) else { return text }
        let ns = text as NSString
        var result = ""
        var cursor = 0
        for match in regex.matches(in: text, options: [], range: NSRange(location: 0, length: ns.length)) {
            let upTo = NSRange(location: cursor, length: match.range.location - cursor)
            result += ns.substring(with: upTo)
            let hex = ns.substring(with: match.range(at: 1))
            if let value = UInt32(hex, radix: 16), let scalar = UnicodeScalar(value) {
                result.append(Character(scalar))
            }
            cursor = match.range.location + match.range.length
        }
        result += ns.substring(from: cursor)
        return result
    }

    private static func parseTitle(_ html: String) -> String {
        if let desc = capture(#"<meta\s+property="og:description"\s+content="([^"]+)""#, in: html) {
            let cleaned = decodeEntities(desc.trimmingCharacters(in: .whitespacesAndNewlines))
            if !cleaned.isEmpty { return cleaned }
        }
        if let topic = capture(#"id="room-topic"[^>]*>(.*?)</div>"#, in: html) {
            let cleaned = topic.trimmingCharacters(in: .whitespacesAndNewlines)
            if !cleaned.isEmpty { return cleaned }
        }
        return "Live stream"
    }

    private static func parseAvatar(_ html: String) -> String? {
        capture(#"<meta\s+property="og:image"\s+content="([^"]+)""#, in: html)
    }

    private static func parseDisplayName(_ html: String) -> String? {
        // Prefer og:title, e.g. "Watch Sweetsweet__Baby live on Chaturbate!" —
        // the <title> is the generic homepage title for room pages.
        if let og = capture(#"<meta\s+property="og:title"\s+content="([^"]+)""#, in: html),
           og.contains("on Chaturbate") {
            var name = og
                .replacingOccurrences(of: " - Chaturbate", with: "")
                .replacingOccurrences(of: " on Chaturbate!", with: "")
                .replacingOccurrences(of: " on Chaturbate", with: "")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            for prefix in ["Watch live ", "Watch "] {
                if name.hasPrefix(prefix) {
                    name = String(name.dropFirst(prefix.count))
                    break
                }
            }
            name = Self.decodeEntities(name)
            if !name.isEmpty { return name }
        }
        if let title = capture("<title>(.*?)</title>", in: html) {
            let cleaned = title
                .replacingOccurrences(of: " - Chaturbate", with: "", options: .caseInsensitive)
                .trimmingCharacters(in: .whitespacesAndNewlines)
            if !cleaned.isEmpty { return cleaned }
        }
        return nil
    }

    private static func decodeEntities(_ text: String) -> String {
        var s = text
        let map: [(String, String)] = [
            ("&amp;", "&"),
            ("&quot;", "\""),
            ("&#39;", "'"),
            ("&lt;", "<"),
            ("&gt;", ">"),
            ("&nbsp;", " ")
        ]
        for (entity, char) in map {
            s = s.replacingOccurrences(of: entity, with: char)
        }
        return s
    }

    /// Returns the first capture group match, or the whole match when no groups.
    private static func capture(_ pattern: String, in text: String) -> String? {
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return nil }
        let ns = text as NSString
        guard let match = regex.firstMatch(in: text, range: NSRange(location: 0, length: ns.length)) else {
            return nil
        }
        if match.numberOfRanges > 1 {
            let range = match.range(at: 1)
            guard range.location != NSNotFound else { return nil }
            return ns.substring(with: range)
        }
        return ns.substring(with: match.range)
    }
}

// MARK: - Kick

struct KickProvider: ChannelProvider {
    var watchURL: (String) -> String { { "https://kick.com/\($0)" } }

    func playbackURL(login: String) async throws -> String? {
        // streamlink has a native kick plugin — the channel page URL is enough.
        watchURL(login)
    }

    func resolveChannel(login: String) async throws -> StreamChannel? {
        guard let json = try await fetchChannel(login: login) else { return nil }
        return StreamChannel(
            id: "kick:\(login)",
            login: login,
            displayName: Self.displayName(from: json) ?? login,
            platform: .kick,
            profileImageURL: Self.profilePic(from: json)
        )
    }

    func getStatus(login: String) async throws -> ChannelStatus? {
        guard let json = try await fetchChannel(login: login) else { return nil }
        let liveObject = json["livestream"] as? [String: Any]
        let playbackURL = json["playback_url"] as? String ?? ""
        var live = liveObject != nil
        if !live {
            live = await playbackIsLive(playbackURL: playbackURL)
        }
        let displayName = Self.displayName(from: json) ?? login
        return ChannelStatus(
            isLive: live,
            title: liveObject?["session_title"] as? String ?? (live ? displayName : ""),
            game: Self.categoryName(from: liveObject ?? [:]),
            profileImageURL: Self.profilePic(from: json)
        )
    }

    func getStreamInfo(login: String) async throws -> StreamInfo? {
        guard let json = try await fetchChannel(login: login) else { return nil }
        if let live = json["livestream"] as? [String: Any] {
            return StreamInfo(
                title: live["session_title"] as? String ?? "Live stream",
                game: Self.categoryName(from: live),
                viewerCount: live["viewer_count"] as? Int ?? 0,
                thumbnailURL: "",
                streamM3U8: "",
                accessToken: nil,
                profileImageURL: Self.profilePic(from: json)
            )
        }
        let playbackURL = json["playback_url"] as? String ?? ""
        guard await playbackIsLive(playbackURL: playbackURL) else { return nil }
        return StreamInfo(
            title: Self.displayName(from: json) ?? login,
            game: "",
            viewerCount: 0,
            thumbnailURL: "",
            streamM3U8: "",
            accessToken: nil,
            profileImageURL: Self.profilePic(from: json)
        )
    }

    /// Reliable live fallback for when the channels endpoint omits `livestream`
    /// (that happens for unauthenticated requests). Probing the playback HLS master
    /// playlist is authoritative and the playback service isn't behind Cloudflare:
    /// live channels serve an EXT-X master playlist, offline channels return 404.
    private func playbackIsLive(playbackURL: String?) async -> Bool {
        guard let raw = playbackURL,
              raw.hasPrefix("http://") || raw.hasPrefix("https://"),
              let url = URL(string: raw) else { return false }
        var request = URLRequest(url: url)
        request.timeoutInterval = 15
        request.setValue(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
            forHTTPHeaderField: "User-Agent")
        guard let (data, response) = try? await KickSession.httpSession.data(for: request),
              let http = response as? HTTPURLResponse, http.statusCode == 200 else { return false }
        let body = String(data: data, encoding: .utf8) ?? ""
        return body.contains("EXT-X-STREAM-INF")
            || body.contains("EXT-X-MEDIA")
            || body.contains("EXTINF")
    }

    /// Channel JSON from the public API. `nil` only when the channel doesn't exist;
    /// rate-limited / server errors throw so the channel isn't dropped as offline.
    private func fetchChannel(login: String) async throws -> [String: Any]? {
        let url = URL(string: "https://kick.com/api/v2/channels/\(login)")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 20
        request.setValue(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
            forHTTPHeaderField: "User-Agent")
        if let header = KickSession.cookieHeader {
            request.setValue(header, forHTTPHeaderField: "Cookie")
        }
        let (data, response) = try await KickSession.httpSession.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw URLError(.badServerResponse)
        }
        if http.statusCode == 404 { return nil }
        guard http.statusCode == 200,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw URLError(.resourceUnavailable)
        }
        return obj
    }

    private static func profilePic(from json: [String: Any]) -> String {
        ((json["user"] as? [String: Any])?["profile_pic"] as? String) ?? ""
    }

    private static func displayName(from json: [String: Any]) -> String? {
        if let name = (json["user"] as? [String: Any])?["username"] as? String, !name.isEmpty {
            return name
        }
        if let slug = json["slug"] as? String, !slug.isEmpty { return slug }
        return nil
    }

    private static func categoryName(from live: [String: Any]) -> String {
        guard let first = (live["categories"] as? [[String: Any]])?.first,
              let name = first["name"] as? String else { return "" }
        return name
    }
}