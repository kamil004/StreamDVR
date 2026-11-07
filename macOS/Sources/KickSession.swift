import Foundation

/// Persisted Kick session (cookie jar) captured from the in-app login WebView.
/// Recording works without it — signing in is an opt-in for ad behavior etc.
enum KickSession {
    static let storageKey = "kick_cookies"

    /// Dedicated ephemeral session so only the explicit Cookie header is sent —
    /// plus the shared URLSession cookie jar doesn't interfere.
    static let httpSession: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        return URLSession(configuration: config)
    }()

    static var cookies: [String: String] {
        guard let raw = ConfigStore.load(key: storageKey),
              let data = raw.data(using: .utf8),
              let dict = try? JSONDecoder().decode([String: String].self, from: data) else {
            return [:]
        }
        return dict
    }

    static var isLoggedIn: Bool { !cookies.isEmpty }

    static var cookieHeader: String? {
        let parts = cookies.map { "\($0.key)=\($0.value)" }
        return parts.isEmpty ? nil : parts.joined(separator: "; ")
    }

    static func save(_ newCookies: [String: String]) {
        guard !newCookies.isEmpty else { return }
        if let data = try? JSONEncoder().encode(newCookies),
           let json = String(data: data, encoding: .utf8) {
            ConfigStore.save(key: storageKey, value: json)
        }
    }

    static func clear() {
        ConfigStore.delete(key: storageKey)
    }

    /// Fetches the signed-in username (nil when not logged in / request fails).
    static func fetchUsername() async -> String? {
        guard isLoggedIn else { return nil }
        let url = URL(string: "https://kick.com/api/v2/users/me")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 15
        request.setValue(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
            forHTTPHeaderField: "User-Agent")
        if let header = cookieHeader {
            request.setValue(header, forHTTPHeaderField: "Cookie")
        }
        guard let (data, response) = try? await httpSession.data(for: request),
              let http = response as? HTTPURLResponse, http.statusCode == 200,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return obj["username"] as? String
    }
}