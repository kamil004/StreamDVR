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

    private static var releasesURL: URL {
        URL(string: "https://api.github.com/repos/\(repoOwner)/\(repoName)/releases")!
    }

    static func stripV(_ s: String) -> String {
        var r = s
        while r.hasPrefix("v") || r.hasPrefix("-") { r = String(r.dropFirst()) }
        return r
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

    /// Newest macOS release asset across ALL releases. Scans the full list
    /// (not just `releases/latest`) so Windows-only releases never shadow the
    /// macOS updater's source. The version is taken from the asset name
    /// (e.g. `StreamDVR-macOS-v1.1.3.zip` → 1.1.3), independent of the tag.
    static func fetchLatest() async -> UpdateInfo? {
        var request = URLRequest(url: releasesURL)
        request.timeoutInterval = 20
        request.setValue("StreamDVR/1.0 (macOS)", forHTTPHeaderField: "User-Agent")
        guard let (data, response) = try? await URLSession.shared.data(for: request),
              let http = response as? HTTPURLResponse, http.statusCode == 200,
              let releases = (try? JSONSerialization.jsonObject(with: data)) as? [[String: Any]] else {
            return nil
        }

        struct Candidate {
            let published: Date
            let info: UpdateInfo
        }

        var best: Candidate? = nil
        for release in releases {
            guard let assets = release["assets"] as? [[String: Any]],
                  let asset = assets.first(where: {
                      let name = $0["name"] as? String ?? ""
                      return name.hasPrefix(assetPrefix) && name.hasSuffix(".zip")
                  }),
                  let assetName = asset["name"] as? String,
                  let urlStr = asset["browser_download_url"] as? String,
                  let url = URL(string: urlStr) else {
                continue
            }
            var version = assetName
                .replacingOccurrences(of: assetPrefix, with: "")
                .replacingOccurrences(of: ".zip", with: "")
            version = stripV(version)
            guard !version.isEmpty else { continue }

            let published = (release["published_at"] as? String)
                .flatMap { ISO8601DateFormatter().date(from: $0) }
                ?? .distantPast
            let info = UpdateInfo(
                version: version,
                tag: (release["tag_name"] as? String) ?? "",
                assetURL: url,
                assetName: assetName)
            if best == nil || published > best!.published {
                best = Candidate(published: published, info: info)
            }
        }
        return best?.info
    }

    /// Downloads the release zip to a temporary location.
    static func download(_ info: UpdateInfo) async throws -> URL {
        var request = URLRequest(url: info.assetURL)
        request.setValue("StreamDVR/1.0 (macOS)", forHTTPHeaderField: "User-Agent")
        let (zipURL, _) = try await URLSession.shared.download(for: request)
        return zipURL
    }
}