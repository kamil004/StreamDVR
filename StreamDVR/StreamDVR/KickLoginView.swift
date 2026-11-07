import SwiftUI
import WebKit
import AppKit

// MARK: - WebView-based Kick login (session cookies, optional)

struct KickLoginView: View {
    @EnvironmentObject var monitor: StreamMonitor
    @Environment(\.dismiss) var dismiss
    @StateObject private var bridge = KickLoginBridge()

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: "person.crop.circle.badge.checkmark")
                    .foregroundColor(.green)
                Text("Sign in to Kick")
                    .font(.headline)
                Spacer()
                Button("Cancel") { dismiss() }
                    .buttonStyle(.borderless)
            }
            .padding(12)
            Divider()

            KickLoginWebView(bridge: bridge) { cookies in
                DispatchQueue.main.async {
                    monitor.completeKickLogin(cookies: cookies)
                    dismiss()
                }
            }
            .frame(minWidth: 620, minHeight: 560)

            Button("I'm signed in — save session") {
                bridge.finish()
            }
            .buttonStyle(.borderedProminent)
            .tint(.green)
            .padding(10)

            Text("Optional: Kick streams record fine without logging in. Sessions are stored locally and sent only to Kick.")
                .font(.caption2)
                .foregroundColor(.secondary)
                .padding(.bottom, 8)
        }
        .frame(width: 680, height: 640)
    }
}

/// Lets SwiftUI ask the WebView's coordinator to save the current session.
final class KickLoginBridge: ObservableObject {
    weak var coordinator: KickLoginWebView.Coordinator?

    func finish() {
        coordinator?.finishManual()
    }
}

struct KickLoginWebView: NSViewRepresentable {
    let bridge: KickLoginBridge
    let onLogin: ([String: String]) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(bridge: bridge, onLogin: onLogin)
    }

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default()

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.customUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

        context.coordinator.webView = webView
        context.coordinator.startPolling()
        webView.load(URLRequest(url: URL(string: "https://kick.com/login")!))
        return webView
    }

    func updateNSView(_ nsView: WKWebView, context: Context) {}

    final class Coordinator: NSObject, WKNavigationDelegate {
        weak var bridge: KickLoginBridge?
        let onLogin: ([String: String]) -> Void
        weak var webView: WKWebView?
        private var timer: Timer?
        private var completed = false

        init(bridge: KickLoginBridge, onLogin: @escaping ([String: String]) -> Void) {
            self.bridge = bridge
            self.onLogin = onLogin
            super.init()
            bridge.coordinator = self
        }

        func startPolling() {
            timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
                self?.checkAutoComplete()
            }
        }

        /// Auto-finishes once the user logs in and Kick redirects off the login page.
        func checkAutoComplete() {
            guard !completed, let webView, let url = webView.url else { return }
            let path = url.path.lowercased()
            let blocked = ["login", "register", "signup", "forgot", "verify", "password", "auth"]
            guard !blocked.contains(where: { path.contains($0) }) else { return }

            WKWebsiteDataStore.default().httpCookieStore.getAllCookies { [weak self] webCookies in
                guard let self = self, !self.completed else { return }
                let hasCookies = (HTTPCookieStorage.shared.cookies ?? []).contains { $0.domain.contains("kick.com") }
                    || webCookies.contains { $0.domain.contains("kick.com") }
                if hasCookies { self.finishManual() }
            }
        }

        func finishManual() {
            guard !completed else { return }
            completed = true
            timer?.invalidate()
            timer = nil

            WKWebsiteDataStore.default().httpCookieStore.getAllCookies { [weak self] webCookies in
                guard let self = self else { return }
                var dict: [String: String] = [:]
                for cookie in (HTTPCookieStorage.shared.cookies ?? []) + webCookies
                where cookie.domain.contains("kick.com") {
                    dict[cookie.name] = cookie.value
                }
                self.onLogin(dict)
            }
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            // Mirror WebView cookies into the shared storage for detection.
            WKWebsiteDataStore.default().httpCookieStore.getAllCookies { cookies in
                for cookie in cookies {
                    HTTPCookieStorage.shared.setCookie(cookie)
                }
                self.checkAutoComplete()
            }
        }
    }
}