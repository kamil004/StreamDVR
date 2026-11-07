#!/usr/bin/env python3
"""Twitch HLS recorder PoC - pure Python + curl (no ffmpeg).

Usage:
  TWITCH_TOKEN=<oauth_or_authtoken> python3 twitch_rec_poc.py <channel> [seconds] [out.ts]

1) Get PlaybackAccessToken (GraphQL, anonymous or authenticated).
2) Resolve HLS via usher + choose best variant.
3) Download .ts segments with curl, appended into one .ts file.
Reports whether Twitch injected ad markers / ad sub-playlists.
"""
import json
import os
import re
import shlex
import subprocess
import sys
import time
import urllib.parse
import urllib.request

CLIENT_ID = "kimne78kx3ncx6brgo4mv6wki5h1ko"
TOKEN = os.environ.get("TWITCH_TOKEN", "")
GQL = "https://gql.twitch.tv/gql"
USHER = "https://usher.ttvnw.net/api/channel/hls/{}.m3u8"
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")


def http_json(url, data=None, headers=None):
    h = {"User-Agent": UA}
    h.update(headers or {})
    req = urllib.request.Request(url, data=data, headers=h)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode("utf-8"))


def http_text(url, headers=None):
    h = {"User-Agent": UA}
    h.update(headers or {})
    req = urllib.request.Request(url, headers=h)
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read().decode("utf-8")


def auth_headers():
    h = {"Client-ID": CLIENT_ID}
    if TOKEN:
        h["Authorization"] = "OAuth " + TOKEN
    return h


def get_access_token(channel):
    q = {
        "query": """
        query PlaybackAccessToken($login: String!, $platform: String!, $playerType: String!) {
          streamPlaybackAccessToken(channelName: $login,
            params: {platform: $platform, playerBackend: "mediaplayer", playerType: $playerType}) {
            value signature __typename
          }
        }""",
        "variables": {"login": channel, "platform": "web", "playerType": "site"},
    }
    resp = http_json(GQL, data=json.dumps(q).encode(), headers=auth_headers())
    data = resp["data"]["streamPlaybackAccessToken"]
    if not data or not data.get("value"):
        raise RuntimeError("No playback token (channel offline or region blocked?)")
    return data["signature"], data["value"]


def get_master(sig, tok, channel):
    base = USHER.format(channel)
    params = {
        "player": "twitchweb",
        "allow_audio_only": "true",
        "allow_source": "true",
        "type": "any",
        "fast_bread": "true",
        "p": str(int(time.time() * 1000)),
        "sig": sig,
        "token": tok,
    }
    url = base + "?" + urllib.parse.urlencode(params)
    return http_text(url, headers=auth_headers())


def pick_best_variant(master):
    variants = []
    for m in re.finditer(
        r"#EXT-X-STREAM-INF:[^\n]*RESOLUTION=(\d+)x(\d+)[^\n]*\n([^\n]+)", master
    ):
        width, height, uri = int(m.group(1)), int(m.group(2)), m.group(3)
        variants.append((width * height, uri))
    variants.sort(reverse=True)
    if not variants:
        raise RuntimeError("No variants in master playlist")
    print(f"picked variant {variants[0][0]} (max {len(variants)} total)")
    return variants[0][1]


def has_ads(master_text):
    markers = []
    for line in master_text.splitlines():
        l = line.lower()
        if any(k in l for k in ("slatetemplate", "#ext-x-slayer", "/slate", "commercial")):
            markers.append(line)
    return markers


def download_segments(media_url, out_path, seconds, headers):
    seen = set()
    got = 0.0
    with open(out_path, "wb") as out:
        t0 = time.time()
        while got < seconds:
            playlist = http_text(media_url, headers=headers)
            for m in re.finditer(r"#EXTINF:\s*([0-9.]+)\s*,[^\n]*\n\s*([^\n]+)", playlist):
                dur = float(m.group(1))
                seg = m.group(2)
                if seg in seen:
                    continue
                seen.add(seg)
                r = subprocess.run(
                    ["curl", "-sS", "--http1.1", "--location", "--max-time", "45",
                     "-A", UA, "-H", "Client-ID: " + CLIENT_ID, seg],
                    capture_output=True,
                    timeout=50,
                )
                if r.returncode != 0 or not r.stdout:
                    print(f"  segment failed: {seg!r} rc={r.returncode}")
                    time.sleep(2)
                    continue
                out.write(r.stdout)
                got += dur
                print(f"  seg {dur:5.2f}s  file={out.tell()//1024} KB  elapsed={time.time()-t0:5.1f}s")
                if got >= seconds:
                    break
            time.sleep(1)
    return got


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    channel = sys.argv[1]
    seconds = float(sys.argv[2]) if len(sys.argv) > 2 else 30.0
    out = sys.argv[3] if len(sys.argv) > 3 else f"{channel}_poc.ts"
    headers = auth_headers()

    print(f"[1/3] playback token for {channel} ...")
    sig, tok = get_access_token(channel)
    print("  token acquired")

    print("[2/3] master playlist ...")
    master = get_master(sig, tok, channel)
    ads = has_ads(master)
    print(f"  ad markers in master: {len(ads)}")
    media = pick_best_variant(master)
    if media.startswith("/"):
        media = "https://usher.ttvnw.net" + media
    elif not media.startswith("http"):
        media = "https://usher.ttvnw.net/" + media
    print(f"  media url: {media[:80]}...")

    print(f"[3/3] recording {seconds}s -> {out}")
    got = download_segments(media, out, seconds, headers)
    print(f"done: {got:6.2f}s of TS written to {out}")

    if TOKEN:
        print("note: recorded WITH OAuth token")


if __name__ == "__main__":
    main()