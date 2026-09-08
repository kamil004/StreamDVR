import SwiftUI
import WebKit
import AppKit

// MARK: - WebView-based Twitch login

struct TwitchLoginView: View {
    @EnvironmentObject var monitor: StreamMonitor
    @Environment(\.dismiss) var dismiss

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: "person.crop.circle.badge.checkmark")
                    .foregroundColor(.purple)
                Text("Sign in to Twitch")
                    .font(.headline)
                Spacer()
                Button("Close") { dismiss() }
                    .buttonStyle(.borderless)
            }
            .padding(12)
            Divider()

            TwitchLoginWebView { token, username in
                DispatchQueue.main.async {
                    monitor.completeWebLogin(token: token, username: username)
                    dismiss()
                }
            }
            .frame(minWidth: 620, minHeight: 560)

            Text("Connecting: use 127.0.0.1 • sessions are sent to Twitch's official login page only")
                .font(.caption2)
                .foregroundColor(.secondary)
                .padding(6)
        }
        .frame(width: 680, height: 640)
    }
}

struct TwitchLoginWebView: NSViewRepresentable {
    var onLogin: (String, String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onLogin: onLogin) }

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default()

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.customUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

        context.coordinator.webView = webView
        context.coordinator.startPolling()
        webView.load(URLRequest(url: URL(string: "https://www.twitch.tv/login")!))
        return webView
    }

    func updateNSView(_ nsView: WKWebView, context: Context) {}

    class Coordinator: NSObject, WKNavigationDelegate {
        var onLogin: (String, String) -> Void
        weak var webView: WKWebView?
        private var timer: Timer?
        private var completed = false

        init(onLogin: @escaping (String, String) -> Void) {
            self.onLogin = onLogin
        }

        func startPolling() {
            timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
                self?.checkForToken()
            }
        }

        func checkForToken() {
            guard !completed else { return }

            let cookies = HTTPCookieStorage.shared.cookies ?? []
            var authToken: String?
            var loginName: String?
            var name: String?

            for cookie in cookies where cookie.domain.contains("twitch.tv") {
                switch cookie.name {
                case "auth-token":
                    if cookie.value.isEmpty == false { authToken = cookie.value }
                case "login":
                    if cookie.value.isEmpty == false { loginName = cookie.value }
                case "name":
                    if cookie.value.isEmpty == false { name = cookie.value }
                default:
                    break
                }
            }

            // Also check the WebView's own cookie store (covers set-cookie on navigation)
            if authToken == nil {
                WKWebsiteDataStore.default().httpCookieStore.getAllCookies { [weak self] webCookies in
                    guard let self = self, !self.completed else { return }
                    for c in webCookies where c.domain.contains("twitch.tv") {
                        if c.name == "auth-token", c.value.isEmpty == false {
                            self.finish(token: c.value, username: self.bestUsername(login: loginName, name: name, fromCookies: webCookies))
                            return
                        }
                    }
                }
                return
            }

            finish(token: authToken!, username: bestUsername(login: loginName, name: name, fromCookies: cookies))
        }

        private func bestUsername(login: String?, name: String?, fromCookies: [HTTPCookie]) -> String {
            let loginName = login ?? fromCookies.first(where: { $0.name == "login" })?.value
            let displayName = name ?? fromCookies.first(where: { $0.name == "name" })?.value
            return displayName ?? loginName ?? "TwitchUser"
        }

        private func finish(token: String, username: String) {
            guard !completed else { return }
            completed = true
            timer?.invalidate()
            timer = nil
            onLogin(token, username)
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            // After the page finishes loading, sync cookies into HTTPCookieStorage
            WKWebsiteDataStore.default().httpCookieStore.getAllCookies { cookies in
                for cookie in cookies {
                    HTTPCookieStorage.shared.setCookie(cookie)
                }
                self.checkForToken()
            }
        }
    }
}
