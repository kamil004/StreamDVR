import Foundation
import Network
import AppKit

// MARK: - Models

enum RecordingStatus: Equatable {
    case idle
    case monitoring
    case recording(duration: TimeInterval)
    case error(String)

    var label: String {
        switch self {
        case .idle: return "Idle"
        case .monitoring: return "Monitoring..."
        case .recording(let d): return String(format: "Recording %02d:%02d:%02d", Int(d)/3600, Int(d)%3600/60, Int(d)%60)
        case .error(let m): return "Error: \(m)"
        }
    }

    var isActive: Bool {
        if case .recording = self { return true }
        return false
    }
}

struct StreamInfo {
    let title: String
    let game: String
    let viewerCount: Int
    let thumbnailURL: String
    let streamM3U8: String
    let accessToken: String?
    let profileImageURL: String
}

// MARK: - Twitch API

actor TwitchAPI {
    static let shared = TwitchAPI()

    // Twitch's public web Client-ID (non-secret). Third-party tools use this as a default
    // so no manual registration is required. Works with both anonymous and logged-in API calls.
    static let defaultClientID = "kimne78kx3ncx6brgo4mv6wki5h1ko"

    private var clientID: String
    private var accessToken: String?
    private var loggedInUsername: String?
    private let baseURL = "https://api.twitch.tv/helix"

    init(clientID: String? = nil) {
        self.clientID = clientID ?? Self.defaultClientID
        self.accessToken = ConfigStore.load(key: "twitch_access_token")
        if let user = ConfigStore.load(key: "twitch_username") {
            self.loggedInUsername = user
        }
    }

    func setClientID(_ id: String) {
        self.clientID = id.isEmpty ? Self.defaultClientID : id
    }

    func setAccessToken(_ token: String?) {
        self.accessToken = token
        if let t = token {
            ConfigStore.save(key: "twitch_access_token", value: t)
        } else {
            ConfigStore.delete(key: "twitch_access_token")
        }
    }

    func reloadTokenFromStore() {
        accessToken = ConfigStore.load(key: "twitch_access_token")
        loggedInUsername = ConfigStore.load(key: "twitch_username")
    }

    func getAccessToken() -> String? { accessToken }

    func setUsername(_ username: String?) {
        self.loggedInUsername = username
        if let u = username {
            ConfigStore.save(key: "twitch_username", value: u)
        } else {
            ConfigStore.delete(key: "twitch_username")
        }
    }

    func getUsername() -> String? { loggedInUsername }

    func isLoggedIn() -> Bool { accessToken != nil }

    func request(path: String) async throws -> (Data, HTTPURLResponse) {
        let components = URLComponents(string: "\(baseURL)\(path)")!
        let url = components.url!
        var request = URLRequest(url: url)
        request.setValue(clientID, forHTTPHeaderField: "Client-ID")
        if let token = accessToken {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let httpResp = response as? HTTPURLResponse else {
            throw NSError(domain: "TwitchAPI", code: -1)
        }
        return (data, httpResp)
    }

    func getUser(login: String) async throws -> StreamChannel? {
        let query = """
        query { user(login: "\(login)") { id login displayName profileImageURL(width: 300) } }
        """
        let data = try await graphQL(query: query)
        let decoded = try JSONDecoder().decode(GQLUserResponse.self, from: data)
        guard let user = decoded.data.user else { return nil }
        return StreamChannel(
            id: "twitch:\(user.login)",
            login: user.login,
            displayName: user.displayName,
            platform: .twitch,
            profileImageURL: user.profileImageURL ?? ""
        )
    }

    func getStream(login: String) async throws -> StreamInfo? {
        let query = """
        query { user(login: "\(login)") { id stream { type title game { displayName } } profileImageURL(width: 300) } }
        """
        let data = try await graphQL(query: query)
        let decoded = try JSONDecoder().decode(GQLStreamResponse.self, from: data)

        // If the API could not resolve the user, or streams list is present (live), decide accordingly.
        guard let user = decoded.data.user, let stream = user.stream else { return nil }
        guard stream.type.lowercased() == "live" else { return nil }

        return StreamInfo(
            title: stream.title,
            game: stream.game?.displayName ?? "",
            viewerCount: 0,
            thumbnailURL: "",
            streamM3U8: "",
            accessToken: nil,
            profileImageURL: user.profileImageURL ?? ""
        )
    }

    /// Channel status ALWAYS returned (even when offline), including the avatar.
    /// `nil` only when the user doesn't exist.
    func getChannelStatus(login: String) async throws -> ChannelStatus? {
        let query = """
        query { user(login: "\(login)") { id stream { type title game { displayName } } profileImageURL(width: 300) } }
        """
        let data = try await graphQL(query: query)
        let decoded = try JSONDecoder().decode(GQLStreamResponse.self, from: data)
        guard let user = decoded.data.user else { return nil }
        let isLive = user.stream?.type.lowercased() == "live"
        return ChannelStatus(
            isLive: isLive,
            title: user.stream?.title ?? "",
            game: user.stream?.game?.displayName ?? "",
            profileImageURL: user.profileImageURL ?? ""
        )
    }

    private func graphQL(query: String) async throws -> Data {
        var comps = URLComponents(string: "https://gql.twitch.tv/gql")!
        comps.queryItems = [URLQueryItem(name: "query", value: "")]
        let url = URL(string: "https://gql.twitch.tv/gql")!

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(clientID, forHTTPHeaderField: "Client-ID")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["query": query])

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw NSError(domain: "TwitchAPI", code: -1, userInfo: [NSLocalizedDescriptionKey: "GraphQL request failed"])
        }
        return data
    }
}

// MARK: - GraphQL responses

struct GQLUserResponse: Decodable {
    struct Data: Decodable {
        struct User: Decodable {
            let id: String
            let login: String
            let displayName: String
            let profileImageURL: String?
        }
        let user: User?
    }
    let data: Data
}

struct GQLStreamResponse: Decodable {
    struct Data: Decodable {
        struct User: Decodable {
            struct Stream: Decodable {
                let type: String
                let title: String
                struct Game: Decodable {
                    let displayName: String
                }
                let game: Game?
            }
            let stream: Stream?
            let profileImageURL: String?
        }
        let user: User?
    }
    let data: Data
}


// MARK: - Decodable Models

struct TwitchUserResponse: Decodable {
    let data: [TwitchUser]
}

struct TwitchUser: Decodable {
    let id: String
    let login: String
    let display_name: String
}

struct TwitchStreamResponse: Decodable {
    let data: [TwitchStream]
}

struct TwitchStream: Decodable {
    let id: String
    let title: String
    let game_name: String
    let viewer_count: Int
    let thumbnail_url: String
    let user_login: String
    let user_id: String
}

struct StreamAccessTokenResponse: Decodable {
    let playback_access_token: PlaybackAccessToken
}

struct PlaybackAccessToken: Decodable {
    let value: String
    let signature: String
}

struct TokenResponse: Decodable {
    let access_token: String
    let expires_in: Int?
    let refresh_token: String?
}

// MARK: - Config storage (plist, no Keychain)

enum ConfigStore {
    private static let fileURL: URL = {
        let fm = FileManager.default
        let base = fm.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
        let dir = base.appendingPathComponent("TwitchDVR", isDirectory: true)
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("settings.plist")
    }()

    private static func loadDict() -> [String: String] {
        guard let data = try? Data(contentsOf: fileURL),
              let dict = try? PropertyListDecoder().decode([String: String].self, from: data) else {
            return [:]
        }
        return dict
    }

    private static func saveDict(_ dict: [String: String]) {
        guard let data = try? PropertyListEncoder().encode(dict) else { return }
        try? data.write(to: fileURL, options: .atomic)
    }

    static func save(key: String, value: String) {
        var dict = loadDict()
        dict[key] = value
        saveDict(dict)
    }

    static func load(key: String) -> String? {
        loadDict()[key]
    }

    static func delete(key: String) {
        var dict = loadDict()
        dict.removeValue(forKey: key)
        saveDict(dict)
    }

    static func loadAll() -> [String: String] {
        loadDict()
    }

    static func replaceAll(with dict: [String: String]) {
        saveDict(dict)
    }
}
