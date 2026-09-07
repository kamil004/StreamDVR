import Foundation

struct UpdateInfo: Equatable {
    let version: String
    let tag: String
    let assetURL: URL
    let assetName: String
}

enum UpdateCheckState: Equatable {
    case idle
    case checking
    case upToDate(current: String, latest: String)
    case updateAvailable(UpdateInfo)
    case downloading(UpdateInfo)
    case error(String)
}

/// Fetches the latest GitHub release and downloads its packaged app.
struct UpdateChecker {
    static let repoOwner = "kamil004"
    static let repoName = "StreamDVR"
    static let assetPrefix = "StreamDVR-macOS"

    private static var latestReleaseURL: URL {
        URL(string: "https://api.github.com/repos/\(repoOwner)/\(repoName)/releases/latest")!
    }

    static func stripV(_ s: String) -> String {
        s.hasPrefix("v") ? String(s.dropFirst()) : s
    }

    /// 1.0.9 < 1.0.15 < 1.1.0
    static func compare(_ a: String, _ b: String) -> ComparisonResult {
        let av = a.components(separatedBy: ".").compactMap { Int($0) }
        let bv = b.components(separatedBy: ".").compactMap { Int($0) }
        for i in 0..<max(av.count, bv.count) {
            let x = i < av.count ? av[i] : 0
            let y = i < bv.count ? bv[i] : 0
            if x != y { return x < y ? .orderedAscending : .orderedDescending }
        }
        return .orderedSame
    }

    /// Latest release information for the packaged macOS asset, or nil when
    /// none exists yet / the request fails.
    static func fetchLatest() async -> UpdateInfo? {
        var request = URLRequest(url: latestReleaseURL)
        request.timeoutInterval = 20
        request.setValue("StreamDVR/1.0 (macOS)", forHTTPHeaderField: "User-Agent")
        guard let (data, response) = try? await URLSession.shared.data(for: request),
              let http = response as? HTTPURLResponse, http.statusCode == 200,
              let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              let tag = json["tag_name"] as? String,
              let assets = json["assets"] as? [[String: Any]] else {
            return nil
        }
        guard let asset = assets.first(where: {
            let name = $0["name"] as? String ?? ""
            return name.hasPrefix(assetPrefix) && name.hasSuffix(".zip")
        }),
        let assetName = asset["name"] as? String,
        let urlStr = asset["browser_download_url"] as? String,
        let url = URL(string: urlStr) else {
            return nil
        }
        return UpdateInfo(version: stripV(tag), tag: tag, assetURL: url, assetName: assetName)
    }

    /// Downloads the release zip to a temporary location.
    static func download(_ info: UpdateInfo) async throws -> URL {
        var request = URLRequest(url: info.assetURL)
        request.setValue("StreamDVR/1.0 (macOS)", forHTTPHeaderField: "User-Agent")
        let (zipURL, _) = try await URLSession.shared.download(for: request)
        return zipURL
    }
}