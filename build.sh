#!/bin/bash
set -e

APP_NAME="StreamDVR"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$SCRIPT_DIR/StreamDVR/StreamDVR"
BUILD_DIR="$SCRIPT_DIR/build"
APP_DIR="$BUILD_DIR/$APP_NAME.app"
CONTENTS_DIR="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"

echo "================================================================"
echo " StreamDVR - macOS Stream Recorder"
echo "================================================================"
echo ""

echo "[1/4] Checking prerequisites..."

if ! command -v swiftc &> /dev/null; then
    echo "  ❌ Swift compiler not found. Install Command Line Tools: xcode-select --install"
    exit 1
fi
echo "  ✓ Swift found: $(swiftc --version 2>&1 | head -1)"

STREAMLINK_FOUND=0
for p in /opt/homebrew/bin/streamlink /usr/local/bin/streamlink /usr/bin/streamlink; do
    if [ -x "$p" ]; then
        STREAMLINK_FOUND=1
        STREAMLINK_PATH="$p"
        break
    fi
done
if command -v streamlink &> /dev/null; then
    STREAMLINK_FOUND=1
    STREAMLINK_PATH="$(command -v streamlink)"
fi

if [ "$STREAMLINK_FOUND" = "1" ]; then
    echo "  ✓ streamlink found: $STREAMLINK_PATH"
else
    echo "  ⚠️  streamlink NOT found. It is REQUIRED for recording."
    if command -v brew &> /dev/null; then
        read -p "  Install via Homebrew? [Y/n]: " -r ANSWER
        echo ""
        if [[ -z "$ANSWER" || "$ANSWER" =~ ^[Yy] ]]; then
            echo "  Installing streamlink via Homebrew..."
            brew install streamlink
            echo "  ✓ streamlink installed"
        else
            echo "  ⚠️  This app needs streamlink. Install later: brew install streamlink"
        fi
    else
        echo "  ⚠️  Homebrew not found. Install streamlink manually or via: brew install streamlink"
    fi
fi

echo ""
echo "[2/4] Building app..."
rm -rf "$BUILD_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"

swiftc \
    "$SRC_DIR/StreamDVRApp.swift" \
    "$SRC_DIR/Platforms.swift" \
    "$SRC_DIR/KickSession.swift" \
    "$SRC_DIR/TwitchAPI.swift" \
    "$SRC_DIR/StreamMonitor.swift" \
    "$SRC_DIR/StreamRecorder.swift" \
    "$SRC_DIR/SleepPreventer.swift" \
    "$SRC_DIR/UpdateChecker.swift" \
    "$SRC_DIR/DependencyInstaller.swift" \
    "$SRC_DIR/TwitchLoginView.swift" \
    "$SRC_DIR/KickLoginView.swift" \
    "$SRC_DIR/ContentView.swift" \
    -o "$MACOS_DIR/$APP_NAME" \
    -framework SwiftUI \
    -framework AppKit \
    -framework WebKit \
    -framework Network \
    -framework IOKit \
    -framework UserNotifications \
    -parse-as-library \
    -O

echo "  ✓ Compiled successfully"

echo ""
echo "[3/4] Creating app bundle..."

# Auto-increment patch version (MAJOR.MINOR.PATCH) on every build.
VERSION_FILE="$SCRIPT_DIR/build_version.txt"
if [ -f "$VERSION_FILE" ]; then
    VERSION="$(cat "$VERSION_FILE")"
else
    VERSION="1.0.0"
fi
IFS='.' read -r MAJ MIN PAT <<< "$VERSION"
PAT=$(( ${PAT:-0} + 1 ))
# Cap patch at 15: when it would exceed, bump minor and reset patch
# (e.g. 1.0.15 -> 1.1.0, 1.1.15 -> 1.2.0).
if [ "$PAT" -gt 15 ]; then
    MIN=$(( ${MIN:-0} + 1 ))
    PAT=0
fi
VERSION="$MAJ.$MIN.$PAT"
echo "$VERSION" > "$VERSION_FILE"
echo "  ✓ Version incremented to $VERSION"

cat > "$CONTENTS_DIR/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>StreamDVR</string>
    <key>CFBundleDisplayName</key>
    <string>StreamDVR</string>
    <key>CFBundleIdentifier</key>
    <string>com.streamdvr.app</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleExecutable</key>
    <string>StreamDVR</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
    <key>CSBundleRequired</key>
    <string></string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.video</string>
    <key>NSHumanReadableCopyright</key>
    <string>For personal use

Created by Kamil Gorecki</string>
</dict>
</plist>
PLIST

echo "  ✓ Info.plist created"

# Copy app icon if present
if [ -f "$SCRIPT_DIR/streamdvr.icns" ]; then
    cp "$SCRIPT_DIR/streamdvr.icns" "$RESOURCES_DIR/AppIcon.icns"
    echo "  ✓ App icon embedded (streamdvr.icns)"
else
    echo "  ⚠️  No app icon found — using default icon"
fi

# Header logo (round PNG with transparent background) for the top-left corner
if [ -f "$SCRIPT_DIR/streamdvricon.png" ]; then
    cp "$SCRIPT_DIR/streamdvricon.png" "$RESOURCES_DIR/StreamDVRIcon.png"
    echo "  ✓ Header icon embedded"
fi

# Sign to avoid Gatekeeper issues (ad-hoc)
codesign --force --deep --sign - "$APP_DIR" 2>/dev/null || echo "  ⚠️  Signing skipped"

echo ""
echo "[4/4] Done!"
echo ""
echo "  ✅ App built: $APP_DIR"
echo ""
echo "  Open it by double-clicking in Finder, or run:"
echo "      open \"$APP_DIR\""
echo ""

echo ""
echo "[5/5] Pushing to GitHub (skip with SKIP_GIT_PUSH=1)..."

if [ -z "$SKIP_GIT_PUSH" ]; then
    if ! git -C "$SCRIPT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
        echo "  ⚠️  Not a git repository — skipping push"
    else
        git -C "$SCRIPT_DIR" add -A
        if ! git -C "$SCRIPT_DIR" diff --cached --quiet; then
            if git -C "$SCRIPT_DIR" commit -m "Build v${VERSION}" >/dev/null 2>&1; then
                echo "  ✓ Committed changes (v${VERSION})"
            else
                echo "  ⚠️  Commit failed"
            fi
        fi
        if git -C "$SCRIPT_DIR" push -q origin HEAD; then
            echo "  ✓ Pushed to GitHub"
        else
            echo "  ⚠️  Push failed (network/auth/repo) — build is still OK"
        fi
    fi
else
    echo "  ✓ Skipped (SKIP_GIT_PUSH=1)"
fi

echo "  ====================================================="
echo "  USAGE"
echo "  ====================================================="
echo "  1.  Open the app and click 'Sign in to Twitch' (top right)."
echo "  2.  Enter your Twitch login and password on Twitch's"
echo "      official page shown inside the app."
echo "  3.  Add channels and click 'Start Recording' — streams are"
echo "      auto-recorded at max quality; premium/Prime = no ads."
echo ""
echo "  No tokens or Client IDs are needed. Works immediately."
