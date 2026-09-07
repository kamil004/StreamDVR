# TwitchDVR

Native **macOS** (Swift/AppKit) and **Windows** (WPF/.NET) apps for recording live
streams at maximum quality. Supports **Twitch**, **Chaturbate** and **Kick**, and
automatically starts recording when a tracked channel goes live.

## Features

- **Three platforms** — add a Twitch channel by name, or paste a URL:
  - `https://www.twitch.tv/shroud`
  - `https://kick.com/odablock`
  - `https://chaturbate.com/sweetsweet__baby/` (incl. locale subdomains like `pl.`)
- **Automatic recording** — starts as soon as a tracked channel goes live
- **Maximum quality** — always the best available stream (`best`)
- **Descriptive filenames** — `YYYY-MM-DD_StreamTitle_HH-MM-SS.ts`
- **Clean folder structure** — `TwitchDVR/<platform>/<login>/`, one subfolder per channel
- **Optional Twitch & Kick login** — built-in browser windows capture your session
  (Twitch: ad-free for premium/Prime; Kick: session cookies forwarded to streamlink)
- **Drag to reorder** channels (macOS)
- **Per-channel folder button** — jump straight to a channel's recordings
- **Auto versioning** — every build bumps the patch version

## macOS build

```bash
./build.sh          # bumps version, builds to build/TwitchDVR.app, installs + launches
open build/TwitchDVR.app
```

- Requires macOS 13+, Xcode Command Line Tools and [streamlink](https://streamlink.github.io/) (`brew install streamlink`).
- `build.sh` reads/writes `build_version.txt`, so the app version increments on each build.

## Windows build

```bash
cd Windows
dotnet build TwitchDVR.App/TwitchDVR.App.csproj -c Release        # quick build
powershell -ExecutionPolicy Bypass -File .\build.ps1              # auto-version + single-file exe + zip
```

- Requires .NET SDK 8.0+ and WebView2 Runtime.
- See `Windows/README.md` for details.

## Platform notes

| Platform    | Status check                                   | Recording                                    |
|-------------|------------------------------------------------|----------------------------------------------|
| Twitch      | GraphQL (gql.twitch.tv)                        | streamlink `--twitch-disable-ads`            |
| Chaturbate  | Room HTML — HLS playlist (`m3u8`) present?     | streamlink on the extracted `m3u8` URL       |
| Kick        | `kick.com/api/v2/channels/<slug>`              | streamlink built-in `kick` plugin            |

## Recording behavior

- **Location** — `~/Documents/TwitchDVR/<platform>/<login>/` (macOS) or
  `%USERPROFILE%\Documents\TwitchDVR\<platform>\<login>\` (Windows), changeable in Settings.
- **Auto-start** — polled every ~60 s while monitoring; recording starts when live.
- **Auto-stop** — file is finalized when the stream ends or you press Stop.
- **Tags** — recordings never contain advertisements (Twitch) thanks to streamlink.

## License

For personal use only. Respect each platform's Terms of Service and streamers' copyright.