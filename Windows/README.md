# StreamDVR for Windows — build & run

A Windows WPF companion to the macOS app. Records **Twitch, Chaturbate and Kick**
streams at maximum quality and auto-starts when a tracked channel goes live.

## Features

- **Three platforms** — add a channel by name, or paste a URL:
  - `https://www.twitch.tv/nick`
  - `https://kick.com/nick`
  - `https://chaturbate.com/channel name/` (works with locale subdomains, e.g. `pl.`)
- **Automatic recording** — starts as soon as a tracked channel goes live (polled every 60 s)
- **Descriptive filenames** — `YYYY-MM-DD_StreamTitle_HH-MM-SS.ts`
- **Clean folder structure** — `Documents\StreamDVR\<platform>\<login>\`, one subfolder per channel
- **Twitch / Kick account login** (optional) — built-in WebView2 login windows capture your session
- **Per-channel folder button** — open a channel's recording folder directly from its row

## Prerequisites

- **.NET SDK 8.0+** (or 10.0) — download from <https://dotnet.microsoft.com/download>
- **WebView2 Runtime** — pre-installed on Windows 10/11; if missing, download from <https://developer.microsoft.com/en-us/microsoft-edge/webview2/>

## Quick build (CLI)

```bash
cd Windows
dotnet build TwitchDVR.App/TwitchDVR.App.csproj -c Release
```

Output: `TwitchDVR.App/bin/Release/net8.0-windows/StreamDVR.exe`

## Build with Visual Studio

1. Open `TwitchDVR.App/TwitchDVR.App.csproj` (or the project folder) in Visual Studio 2022+.
2. If prompted, install the **.NET desktop development** workload.
3. Press **F5** to build and run, or **Ctrl+Shift+B** to build.

## One-shot build (auto-version, single-file exe + zip)

`build.ps1` publishes a self-contained single-file `StreamDVR.exe` and zips it.
Unlike the macOS build it does **not** bump the version: it reads the shared
repo-root `build_version.txt` — the same version used by the macOS build — so both
platforms always ship in the same release:

```powershell
cd Windows
powershell -ExecutionPolicy Bypass -File .\build.ps1
```

Output:
- `TwitchDVR.App\bin\Release\net8.0-windows\win-x64\publish\StreamDVR.exe`
- `Windows\StreamDVR-Windows-v<version>.zip` (version matches the macOS release)

It bundles the .NET 8 runtime and WebView2 loader — no .NET install needed. On first
launch the app auto-installs missing `streamlink` and `ffmpeg` via `winget`.

## First run

1. Add channels (name or URL, see above) and click **Start Recording** — or sign into
   Twitch/Kick from the Settings tab first.
2. In the **Channels** tab each row shows the platform badge (Twitch / Chaturbate / Kick),
   online status, stream title and a **📁** button that opens that channel's folder.
3. The **Recordings** tab lists every file under the output directory
   (`Documents\StreamDVR` by default), grouped into `platform\login\` subfolders.

## What gets stored

All settings live in:

```
%APPDATA%\StreamDVR\settings.json
```

Keys: `channels` (JSON list incl. platform), `twitch_access_token` / `twitch_username`
(Twitch web session), `kick_cookies` (Kick session cookie jar), `twitch_output_dir`.

Recordings are saved to `%USERPROFILE%\Documents\StreamDVR\` by default, in
`<platform>\<login>\` subfolders.

## Dependencies

| Component        | Role                                      |
|------------------|-------------------------------------------|
| streamlink       | Streams from Twitch/Kick/Chaturbate (*.ts output). Install via `winget install streamlink`. |
| ffprobe          | Captures resolution + bitrate (bundled with ffmpeg). Install via `winget install Gyan.FFmpeg`. |
| WebView2 Runtime | Embedded browser for Twitch / Kick login. |

## Notes

- Chaturbate has no streamlink plugin anymore — the app extracts the room's HLS playlist
  (`m3u8`) directly from the room HTML and feeds it to streamlink. If the room is offline
  the channel just shows as offline.
- Kick uses streamlink's built-in `kick` plugin; logging in is optional (session cookies
  are forwarded to streamlink via `--http-cookie`).
- `streamlink.exe` and `ffprobe.exe` are discovered on PATH and in the common install
  locations. If not found, the relevant feature shows an error message.
- The settings file contains your Twitch OAuth token and Kick session cookies in plain
  text (no Windows Keychain to encrypt). Keep `%APPDATA%\StreamDVR\settings.json` private.