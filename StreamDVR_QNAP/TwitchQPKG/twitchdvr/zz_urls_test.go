package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

var zzCBURIS = regexp.MustCompile(`URI="([^"]+)"`)

func TestZZWriteURLs(t *testing.T) {
	httpc := &http.Client{}
	var room string
	if env := os.Getenv("CB_ROOM"); env != "" {
		room = env
	} else {
		req, _ := http.NewRequest("GET", "https://chaturbate.com/api/ts/discover/carousels/most_popular/", nil)
		req.Header.Set("User-Agent", chaturbateUA)
		resp, err := httpc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var d struct {
			Rooms []struct {
				Room string `json:"room"`
			} `json:"rooms"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		for _, r := range d.Rooms {
			if r.Room == "" {
				continue
			}
			if live, _, err := chaturbateLive(httpc, r.Room); err == nil && live {
				room = r.Room
				break
			}
		}
		if room == "" {
			t.Fatal("no live room")
		}
	}
	t.Logf("room=%s", room)

	master, base, err := chaturbateMaster(httpc, room)
	if err != nil {
		t.Fatal(err)
	}
	best, err := cbBestStreams(master, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove("/tmp/cb_urls.txt")
	f, err := os.Create("/tmp/cb_urls.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range []string{best.video, best.audio, best.res} {
		fmt.Fprintf(f, "%s\n", l)
	}
	t.Logf("video=%s\naudio=%s\nres=%s", best.video, best.audio, best.res)
	if strings.TrimSpace(best.video) == "" {
		t.Fatal("empty video url")
	}
}
