# StreamDVR — Pełna Analiza Projektu

## 1. Przegląd

**StreamDVR** to aplikacja do automatycznego nagrywania streamów na żywo z platform:
- **Twitch** (GraphQL API + HLS)
- **Kick** (REST API + HLS)
- **Chaturbate** (HLS playlist)

Działa na **3 platformach**: macOS (Swift), Windows (C#/WPF), QNAP NAS (Go + Web UI).

---

## 2. Struktura katalogów

```
StreamDVR/                          ← repo root
├── StreamDVR/                      ← macOS (SwiftUI, kompilacja przez swiftc)
│   └── StreamDVR/                  ← źródła Swift
│       ├── StreamDVRApp.swift      ← punkt wejścia, konfiguracja okna
│       ├── ContentView.swift       ← UI główne (4 tabele: Channels, Recordings, Logs, Settings)
│       ├── StreamMonitor.swift     ← SERCER logiki — monitoring, nagrywanie, statusy
│       ├── StreamRecorder.swift    ← wywołania streamlink/ffprobe, nagrywanie
│       ├── TwitchAPI.swift         ← GraphQL Twitch, weryfikacja tokenu
│       ├── Platforms.swift         ← ChannelProvider protokół (Twitch/Kick/Chaturbate)
│       ├── TwitchLoginView.swift   ← WebKit browser do logowania Twitch
│       ├── KickLoginView.swift     ← WebKit browser do logowania Kick
│       ├── KickSession.swift       ← zapis/odczyt cookies Kick do WKWebsiteDataStore
│       ├── DependencyInstaller.swift ← autoinstalacja streamlink/ffmpeg/Homebrew
│       ├── SleepPreventer.swift    ← IOKit power assertion (zapobiega uśpieniu Maca)
│       └── UpdateChecker.swift     ← sprawdzanie aktualizacji na GitHub Releases
│
├── Windows/                        ← Windows (WPF/.NET 8)
│   ├── README.md
│   ├── build.ps1                   ← skrypt budowania
│   ├── StreamDVR.App/              ← projekt WPF (App.xaml, MainWindow.xaml)
│   │   ├── MainWindow.xaml         ← UI główne (XAML)
│   │   ├── MainWindow.xaml.cs      ← kontroler MainWindow
│   │   ├── StreamMonitor.cs        ← logika monitorowania (odpowiednik StreamMonitor.swift)
│   │   ├── LoginWindow.xaml/.cs    ← logowanie Twitch (WebView2)
│   │   ├── KickLoginWindow.xaml/.cs ← logowanie Kick
│   │   ├── Converters.cs           ← konwersje XAML binding
│   │   └── App.xaml.cs             ← punkt wejścia
│   └── StreamDVR.Core/             ← klasa wspólna (model + API)
│       ├── Models.cs               ← StreamChannel, StreamPlatform, StreamInfo, ChannelStatus
│       ├── Platforms.cs            ← ChannelProvider implementacje (Twitch/Kick/Chaturbate)
│       ├── TwitchApi.cs            ← GraphQL Twitch
│       ├── StreamRecorder.cs       ← streamlink/ffprobe via Process.Start
│       ├── ConfigStore.cs          ← settings.json (Windows) / plist (macOS)
│       ├── KickSession.cs          ← cookies Kick
│       ├── UpdateChecker.cs        ← GitHub Releases checker
│       └── DependencyInstaller.cs  ← winget instalacja (Windows)
│
├── StreamDVR_QNAP/                 ← QNAP NAS (Go + Web UI)
│   ├── TwitchQPKG/                 ← pakiet QPKG (instalator QNAP)
│   │   ├── twitchdvr/              ← źródła Go
│   │   │   ├── main.go             ← HTTP router, API endpoints, konfiguracja
│   │   │   ├── recorder.go         ← Recorder struct, HLS segment download, live streaming
│   │   │   ├── twitch.go           ← Twitch GraphQL, HLS playlist parsing
│   │   │   ├── kick.go             ← Kick API, session cookies, HLS
│   │   │   ├── chaturbate.go       ← Chaturbate HLS (przez ffmpeg)
│   │   │   ├── crypto.go           ← AES-256-GCM szyfrowanie tokenów
│   │   │   ├── avsync.go           ← korekcja synchronizacji audio/wideo
│   │   │   ├── disk_*.go           ← platform-specific disk free
│   │   │   └── web/                ← embedded frontend (index.html, files.html, hls.min.js)
│   │   ├── control/                ← QPKG install/uninstall scripts
│   │   ├── build.sh                ← kompilacja Go + QPKG
│   │   └── build_qpkg.py           ← generowanie .qpkg
│   │
│   ├── StreamDVR-QNAP/             ← .NET Core wersja na QNAP (alternatywna)
│   │   ├── src/StreamDVR.Web/      ← .NET 6/8/10 Web API
│   │   ├── build.sh                ← build .NET
│   │   └── build_qpkg.py           ← QPKG packaging
│   │
│   ├── twitch_poc/                 ← Proof of Concept (Python)
│   └── kick_favicon.png            ← favicon
│
├── build/                          ← output build macOS
├── build.sh                        ← skrypt budowania macOS
├── build_version.txt               ← wersja (auto-increment)
├── payload/                        ← QPKG payload
├── Icon                            ← placeholder
├── README.md
└── screenshot.jpg
```

---

## 3. Platforma macOS — Szczegóły

### Architektura
- **Single-binary Swift** — kompilacja `swiftc` bez Xcode project
- **SwiftUI** z AppKit integracją
- **Brak zewnętrznych zależności** w kodzie — wszystko przez Process wywołania

### Kluczowe pliki

#### `StreamDVRApp.swift` (29 linii)
- Punkt wejścia (`@main`)
- Tworzy `StreamMonitor` jako shared `@StateObject`
- Konfiguruje okno (900x600 min, 1050x700 domyślnie)
- Podłącza `SettingsView` w panelu Settings

#### `ContentView.swift` (951 linii)
- **4 tabe**: Channels (0), Recordings (1), Logs (2), Settings (3)
- **HeaderView** — loginy Twitch/Kick, przycisk Start/Stop monitoring
- **ChannelListView** — dodawanie kanałów (platform picker + URL parser), drag-to-reorder
- **RecordingsView** — lista nagrań
- **LogsView** — logi z kolorami (info/warning/error/success)
- **SettingsView** — konfiguracja folderu, interwału, powiadomień, auto-sort, sleep prevention
- Status bar z licznikami (channels/live/offline/recording) + wersja

#### `StreamMonitor.swift` (957 linii) — **NAJBARDZIEJ KRYTYCZNY PLIK**
- **`@MainActor class StreamMonitor: ObservableObject`** — centralny obiekt stanu
- **`@Published` properties**: `channels`, `recordingStatuses`, `recordingInfo`, `logs`, `isLoggedIn`, `isMonitoring`, `pollInterval`, itp.
- **`sortedChannels`** — auto-sort: nagrywane → live → offline
- **`checkAllChannels()`** — pętla statusu: pobiera status każdego kanału, triggers auto-recording
- **`startRecording()`** — tworzy `StreamRecorder`, pobiera stream URL z provider
- **`probeFile()`** — ffprobe do odczytu rozdzielczości/FPS/bitrate
- **`parseInput()`** — parser URL/nazwy kanału (Twitch/Kick/Chaturbate)
- **`downloadAndInstallUpdate()`** — pobiera ZIP, rozpakowuje, podmienia .app, relaunch
- **`DockBadgeView`** — custom dock tile z badge (licznik aktywnych nagrań)
- **`LiveNotificationDelegate`** — powiadomienia systemowe przy live
- **`knownLiveKeys`** — śledzenie offline→live transition (unikanie spamu)
- **`exportBackup` / `importBackup`** — JSON backup/restore ustawień

#### `StreamRecorder.swift` (263 linii)
- Obiekt `StreamRecorder` — wrapper nad `Process` (streamlink + ffmpeg)
- **`startRecording()`** — uruchamia `streamlink --twitch-disable-ads best -o file.ts`
- **`stop()`** — kończy nagrywanie
- **`convertMP4()`** — ffmpeg remux do MP4 (fast-start)
- Callback `onComplete` zwraca ścieżkę pliku

#### `TwitchAPI.swift` (315 linii)
- GraphQL endpoint: `https://gql.twitch.tv/gql`
- **`isLoggedIn()`** — weryfikacja tokenu
- **`getUsername()`** — pobranie nazwy użytkownika
- **`getStreamInfo()`** — pobranie tytułu/gry/rozdzdzielczości streamu
- **`getPlaybackContext()`** — playback token dla HLS
- **`getMasterPlaylist()`** — pobranie master playlist (HLS variants)

#### `Platforms.swift` (394 linii)
- Protokół `ChannelProvider` — abstrakcja dla 3 platform
- **`TwitchProvider`** — GraphQL status + HLSusher
- **`KickProvider`** — REST API `kick.com/api/v2/channels/<slug>`
- **`ChaturbateProvider`** — HTML parsing (m3u8 obecny?)
- **`resolveChannel()`** — pobranie display name/avatar

#### `DependencyInstaller.swift` (148 linii)
- Autoinstalacja Homebrew → streamlink → ffmpeg
- Wykrywa istniejące instalacje (PATH, /opt/homebrew, /usr/local)
- Pokazuje komunikaty z instrukcjami manualnej instalacji

#### `SleepPreventer.swift` (28 linii)
- IOKit `IOPMAssertionCreate` — zapobiega uśpieniu Maca podczas monitoring

#### `UpdateChecker.swift` (106 linii)
- GitHub Releases API — sprawdza najnowszy build
- Porównanie wersji semantic
- Pobieranie ZIP dla macOS (`StreamDVR-macOS-v*.zip`)

### Konfiguracja
- **Storage**: `~/Library/Application Support/TwitchDVR/settings.plist`
- **Klucze**: `channels`, `twitch_output_dir`, `auto_sort_live`, `prevent_sleep`, `poll_interval`, `live_notifications`, `check_updates`, `twitch_access_token`, `twitch_username`
- **Kanały**: JSON encoded w `channels`
- **Domyślny output**: `~/Documents/StreamDVR/<platform>/<login>/`

### Build
```bash
./build.sh
# Kompiluje swiftc, tworzy .app bundle, auto-increment wersji, codesign, commit+push
```

---

## 4. Platforma Windows — Szczegóły

### Architektura
- **.NET 8 WPF** (XAML + code-behind)
- **Dwa projekty**: `StreamDVR.App` (UI) + `StreamDVR.Core` (logika)
- **WebView2** do logowania Twitch/Kick
- **winget** do instalacji streamlink/ffmpeg

### Kluczowe pliki

#### `StreamDVR.App/MainWindow.xaml` (395 linii)
- XAML UI — podobne do macOS (kanały, nagrania, logi, ustawienia)
- DataTemplates dla channel cards
- Bindings do `StreamMonitor`

#### `StreamDVR.App/MainWindow.xaml.cs` (461 linii)
- Code-behind MainWindow
- Event handlers, drag-drop reorder

#### `StreamDVR.App/StreamMonitor.cs` (1038 linii)
- Odpowiednik `StreamMonitor.swift`
- `ObservableCollection<StreamChannel>` + `ObservableDictionary`
- `CheckAllChannels()` — async polling
- `StartRecording()` — tworzy `StreamRecorder`
- Duration timer + ffprobe probing

#### `StreamDVR.App/StreamRecorder.cs` (brak — w Core)
- W Windows wersja jest w `StreamDVR.Core`

#### `StreamDVR.Core/Models.cs` (117 linii)
- `StreamChannel` — INotifyPropertyChanged
- `StreamPlatform` enum (Twitch/Chaturbate/Kick)
- `StreamInfo`, `ChannelStatus`, `RecordingStats`

#### `StreamDVR.Core/Platforms.cs` (569 linii)
- Odpowiednik `Platforms.swift`
- `TwitchPlatform`, `KickPlatform`, `ChaturbatePlatform`
- HTTP + JSON parsing

#### `StreamDVR.Core/TwitchApi.cs` (147 linii)
- GraphQL Twitch API

#### `StreamDVR.Core/ConfigStore.cs` (66 linii)
- Windows: `%APPDATA%\StreamDVR\settings.json`
- `Load()` / `Save()` / `Delete()` / `LoadAll()` / `ReplaceAll()`

#### `StreamDVR.Core/DependencyInstaller.cs` (86 linii)
- `winget install streamlink` / `winget install ffmpeg`

### Konfiguracja
- **Storage**: `%APPDATA%\StreamDVR\settings.json`
- **Kanały**: JSON w `channels`
- **Domyślny output**: `%USERPROFILE%\Documents\StreamDVR\<platform>\<login>\`

### Build
```powershell
cd Windows
powershell -ExecutionPolicy Bypass -File .\build.ps1
# Self-contained single-file StreamDVR.exe + zip
```

---

## 5. Platforma QNAP NAS — Szczegóły

### Architektura
- **Czyste Go** — pojedynczy binary `twitchdvr`
- **Web UI** — embedded HTML/CSS/JS (hls.js)
- **HTTP API** — JSON REST
- **Porty**: HTTP 8090, HTTPS 8443 (self-signed cert auto-generated)
- **QPKG** — pakiet instalacyjny QNAP

### Kluczowe pliki Go

#### `main.go` (1683 linii) — **NAJBARDZIEJ KRYTYCZNY PLIK**
- **`App` struct** — centralny stan: config, recorders, avatars, rate limiting
- **`NewApp()`** — inicjalizacja: load config, encrypted settings, avatars, rate state
- **`monitor()`** — pętla: check live status → start/stop recordings
- **`setupRoutes()`** — HTTP router z ~30 endpointami
- **Endpointy**:
  - `GET /` — UI
  - `GET /api/status` — pełny status (kanały, nagrania, login, dysk)
  - `GET /api/log` — logi
  - `POST /api/login` — Twitch login (token)
  - `POST /api/logout` — logout
  - `POST /api/kick/login` — Kick login
  - `POST /api/kick/logout` — Kick logout
  - `POST /api/channels` — dodaj kanał
  - `DELETE /api/channels/{login}` — usuń kanał
  - `POST /api/channels/{login}/toggle` — pause/resume
  - `POST /api/channels/{login}/start` — nagrywaj teraz
  - `POST /api/channels/{login}/stop` — stop
  - `POST /api/outdir` — zmień folder
  - `POST /api/poll` — zmień interwał (15/30/60/120s)
  - `POST /api/unlock` — unlock settings (password)
  - `POST /api/lock` — lock settings
  - `POST /api/pwset` — ustaw hasło admina
  - `GET /api/export` — backup JSON
  - `POST /api/import` — restore JSON
  - `GET /media/{platform}/{login}/{name}` — serve plik
  - `GET /playlist/{platform}/{login}/{name}` — HLS playlist (growing file)
  - `GET /api/files/{platform}/{login}` — lista plików
  - `DELETE /api/files/...` — delete/clear files
  - `POST /api/files/.../convert` — convert to MP4
  - `POST /api/files/.../rename` — rename file
  - `GET /files/{platform}/{login}` — files panel UI
- **Rate limiting** — anti-spam dla unlock settings (5 taps / 5 min → 10 min block)
- **Admin password** — AES-256-GCM szyfrowanie tokenów w `settings.enc`
- **`loginFromInput()`** — parser URL/nazwy (Twitch/Kick/Chaturbate)
- **`migrateOutDirLayout()`** — migracja ze starego layoutu (flat → platform subfolders)
- **`ensureSelfSignedCert()`** — auto-generacja certyfikatu TLS (ECC P-256)
- **`fixPermissions()`** — chmod 777/666 dla FTP accessibility

#### `recorder.go` (1245 linii)
- **`Recorder` struct** — pojedyncze nagranie (context, stop/done channels, log)
- **`startRec()` / `stopRec()`** — zarządzanie nagraniami
- **`recordLoop()`** — pobieranie segmentów HLS do pliku .ts
- **Segment index** — `.idx` sidecar (JSON) dla live playlist
- **`runTwitch()`** — playback context → master playlist → pick variant → recordLoop
- **`runKick()`** — kick master playlist → kick headers → recordLoop
- **`runChaturbate()`** — ffmpeg capture (LL-HLS video+audio) → parts concat
- **`captureChaturbateFF()`** — ffmpeg z retry logic (token rotation)
- **`convertMP4()`** — ffmpeg remux do MP4 fast-start
- **`finalizeLeftovers()`** — naprawa przerwanych nagrań przy starcie

#### `twitch.go` (303 linii)
- GraphQL Twitch API
- **`fetchLive()`** — status live/offline + tytuł
- **`playbackCtx()`** — playback context + token
- **`masterPlaylistCtx()`** — master playlist parsing
- **`pickVariantInfo()`** — wybór najlepszej jakości (resolution + bitrate)
- **`usherURL()`** — Twitch HLS URL (usher.ttvnw.net)
- **`validateToken()`** — weryfikacja tokena

#### `kick.go` (223 linii)
- **`kickLive()`** — kick.com/api/v2/live/channel
- **`kickMaster()`** — master playlist z kick API
- **`kickHeaders()`** — session cookies headers
- **`kickAvatar()`** — avatar URL
- **`kickValidate()`** — weryfikacja tokena

#### `chaturbate.go` (269 linii)
- **`chaturbateLive()`** — HTML parsing (m3u8 obecny?)
- **`chaturbateMaster()`** — master playlist
- **`cbBestStreams()`** — video + audio stream selection
- **`chaturbateAvatar()`** — avatar

#### `crypto.go` (102 linii)
- **AES-256-GCM** szyfrowanie
- **`storeKey()`** — PBKDF2 key derivation + random salt
- **`deriveKey()`** — PBKDF2 z solą
- **`pack()` / `unpack()`** — encrypt/decrypt

#### `avsync.go` (166 linii)
- Korekcja drift audio/video
- PTS/DTS analysis i compensation

### Web UI
- **`web/index.html`** (802 linii) — ciemny motyw, dashboard z kanałami
- **`web/files.html`** — panel plików/nagrań
- **`web/hls.min.js`** — hls.js do odtwarzania HLS
- **`web/icon.png`** — ikona
- **`web/twitch_favicon.png` / `kick_favicon.png`** — favicons
- **UI Features**:
  - Karty kanałów (avatar, status, tytuł, przyciski)
  - Live timer (REC czas)
  - Panel logów
  - Panel ustawień (login, folder, interwał, hasło admina)
  - Panel plików (lista, delete, rename, convert to MP4)
  - HLS player (hls.js) dla nagrywania w czasie rzeczywistym
  - Anti-spam rate limiting UI

### Konfiguracja
- **Config**: `<dataDir>/config.json`
- **Encrypted settings**: `<dataDir>/settings.enc` (AES-256-GCM)
- **Rate state**: `<dataDir>/rate.json`
- **Kluczowe config keys**: `token`, `username`, `kick_token`, `kick_username`, `out_dir`, `poll_sec`, `auto_mp4`, `admin_pw`, `channels`
- **Domyślny output**: `/share/Public/StreamDVR`

### Build
```bash
cd StreamDVR_QNAP/TwitchQPKG
./build.sh          # go build + QPKG packaging
python3 build_qpkg.py  # generowanie .qpkg
```

---

## 6. Wspólne cechy między platformami

| Cecha | macOS | Windows | QNAP |
|-------|-------|---------|------|
| UI Framework | SwiftUI | WPF/XAML | HTML/CSS/JS |
| Config Storage | plist | JSON | JSON + encrypted |
| Recording Engine | streamlink | streamlink | Go HLS downloader / ffmpeg |
| Twitch API | GraphQL | GraphQL | GraphQL |
| Kick API | REST | REST | REST |
| Chaturbate | HLS parsing | HLS parsing | ffmpeg |
| Login | WebKit browser | WebView2 | API token |
| Dependency Install | Homebrew | winget | N/A (pre-installed) |
| Update System | GitHub Releases | GitHub Releases | Manual QPKG |
| Version | Shared | Shared | Shared (build_version.txt) |
| Output Format | .ts (MP4 optional) | .ts (MP4 optional) | .ts (MP4 optional) |
| Output Path | `<dir>/<platform>/<login>/` | `<dir>/<platform>/<login>/` | `<dir>/<platform>/<login>/` |

---

## 7. Kluczowe wzorce projektowe

### 1. ChannelProvider Pattern (macOS/Windows)
Protokół/interfejs z metodami:
- `getStatus(login) → ChannelStatus`
- `getStreamInfo(login) → StreamInfo`
- `resolveChannel(login) → StreamChannel`
- `watchURL(login) → String`

### 2. Parser URL/nazwy
Wspólna logika parsowania na wszystkich platformach:
- Wykrywanie platformy z URL (twitch.tv, kick.com, chaturbate.com)
- Extrakcja loginu (ostatni segment ścieżki)
- Normalizacja (lowercase, trim)
- Domyślna platforma dla czystych nazw

### 3. Recording Flow
1. Poll → channel is live
2. Get stream URL (HLS master playlist)
3. Pick best variant (resolution + bitrate)
4. Download segments → .ts file
5. On stream end → optional MP4 conversion
6. Store in `<output>/<platform>/<login>/<filename>.ts`

### 4. Status Monitoring Loop
- `checkAllChannels()` co `pollInterval` (15/30/60/120s)
- Offline → Live transition → auto-start recording
- Live → Offline transition → stop recording
- Track `knownLiveKeys` do detekcji zmian

### 5. Encryption (QNAP)
- PBKDF2 key derivation z random salt
- AES-256-GCM szyfrowanie
- Tokeny przechowywane w `settings.enc`, config w `config.json`
- Hasło admina blokuje panel ustawień (nie nagrywanie)

---

## 8. Potencjalne obszary zmian

### Łatwe zmiany
- Interwał pollu (15/30/60/120s)
- Domyślna platforma
- Nazwy plików (format)
- Wygląd UI (kolory, layout)
- Komunikaty logów
- Domyślny folder output

### Średnie zmiany
- Dodanie nowej platformy (np. YouTube, Trovo)
- Zmiana formatu pliku (.ts → .mp4 default)
- Zmiana API endpointów
- Dodanie nowych ustawień
- Zmiana systemu backup/restore

### Złożone zmiany
- Refaktoryzacja StreamMonitor (podział na mniejsze moduły)
- Zmiana engine nagrywania (streamlink → direct HLS download)
- Dodanie bazy danych (SQLite) zamiast plików JSON/plist
- Zmiana systemu logowania (OAuth flow)
- Dodanie multi-threading/recording concurrency
- Zmiana architektury web UI (React/Vue zamiast vanilla JS)

---

## 9. Zależności zewnętrzne

### macOS
- **streamlink** — nagrywanie streamów
- **ffmpeg/ffprobe** — statystyki + konwersja MP4
- **Homebrew** — instalacja zależności
- **IOKit** — sleep prevention
- **WebKit** — login browser
- **UserNotifications** — systemowe powiadomienia

### Windows
- **streamlink** — nagrywanie
- **ffmpeg/ffprobe** — statystyki + konwersja
- **winget** — instalacja zależności
- **WebView2** — login browser

### QNAP
- **ffmpeg** — Chaturbate recording + MP4 conversion
- **hls.js** — HLS playback w UI
- **Go standard library** — wszystko inne (net/http, crypto, encoding/json, etc.)

---

## 10. Zmienne środowiskowe i ścieżki

### macOS
- **Config**: `~/Library/Application Support/TwitchDVR/settings.plist`
- **Output**: `~/Documents/StreamDVR/`
- **streamlink**: `/opt/homebrew/bin/streamlink` lub `/usr/local/bin/streamlink`
- **ffprobe**: `/opt/homebrew/bin/ffprobe` lub `/usr/local/bin/ffprobe`
- **Bundle ID**: `com.streamdvr.app`
- **App Name**: `StreamDVR`

### Windows
- **Config**: `%APPDATA%\StreamDVR\settings.json`
- **Output**: `%USERPROFILE%\Documents\StreamDVR\`
- **streamlink**: `%USERPROFILE%\AppData\Local\streamlink`
- **WebView2**: wbudowany w system

### QNAP
- **Config**: `<dataDir>/config.json`
- **Encrypted**: `<dataDir>/settings.enc`
- **Output**: `/share/Public/StreamDVR/`
- **Data Dir**: `<QPKG_ROOT>/twitchdvr/data/`
- **HTTP Port**: 8090
- **HTTPS Port**: 8443

---

## 11. Versioning

- **Plik**: `build_version.txt` (root repo)
- **Format**: `MAJOR.MINOR.PATCH`
- **Auto-increment**: patch +1 co build, cap na 15 → bump minor
- **Przykład**: `1.0.14 → 1.0.15 → 1.1.0 → 1.1.1`
- **Wspólne**: macOS i Windows dzielą tę samą wersję
- **QNAP**: wersja hardcodowana w `main.go` (`appVersion = "2.6.4"`)

---

## 12. Release Process

1. Build macOS: `./build.sh`
2. Build Windows: `cd Windows; powershell -ExecutionPolicy Bypass -File .\build.ps1`
3. Build QNAP: `cd StreamDVR_QNAP/TwitchQPKG; ./build.sh`
4. GitHub Release z assetami:
   - `StreamDVR-macOS-v<version>.zip`
   - `StreamDVR-Windows-v<version>.zip`
   - `StreamDVR_2.6.4_x86_64.qpkg`

---

## 13. Uwagi techniczne

### Streamlink (macOS/Windows)
- Używa `--twitch-disable-ads` dla Twitch (premium/Prime)
- Pobiera `best` jakość (najwyższa dostępna)
- Format wyjściowy: MPEG-TS (.ts)
- Optional MP4 conversion przez ffmpeg (fast-start)

### Go Recorder (QNAP)
- Własny HLS downloader (bez streamlink)
- Segment indexing (.idx sidecar) dla live playlist
- Retry logic dla token rotation
- Parts concat dla Chaturbate (limit 5 parts)
- Auto-conversion do MP4 (configurable)

### Bezpieczeństwo
- Tokeny Twitch/Kick przechowywane zaszyfrowane (QNAP) lub plaintext (macOS/Windows)
- QNAP: hasło admina blokuje panel ustawień (nie nagrywanie)
- Rate limiting dla unlock settings (anti-brute-force)
- Self-signed TLS dla QNAP HTTPS

---

## 14. Potencjalne problemy i TODO

1. **QNAP wersja .NET** (`StreamDVR-QNAP/`) — wygląda na alternatywną/porzuconą wersję. Sprawdzić czy jest używana.
2. **Duplikacja kodu** — logika jest duplikowana między macOS (Swift) i Windows (C#). Rozważyć shared library.
3. **Hardcoded ścieżki** — ffprobe ścieżka w macOS jest hardcodowana (`/opt/homebrew/bin/ffprobe`).
4. **Brak testów** na macOS/Windows — QNAP ma testy (`.go` test files).
5. **Localization** — UI jest po polsku (QNAP) i angielsku (macOS/Windows). Rozważyć unified i18n.
6. **Error handling** — wiele miejsc z `try?` / `catch {}` — ciche ignorowanie błędów.
7. **Memory leaks** — `StreamMonitor` trzyma wszystkie logi w pamięci (limit 200).

---

*Analiza przygotowana 2026-10-27. Projekt: StreamDVR v2.6.4*
