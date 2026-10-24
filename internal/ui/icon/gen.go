//go:build ignore

// gen extracts the icons the UI uses from an @iconify-json/mingcute
// icons.json into mingcute.json: only the filled ("currentColor") path data,
// since the transparent padding paths carry no pixels.
//
//	go run gen.go path/to/icons.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

var names = []string{
	"add-line", "aiming-2-line", "alert-line", "arrow-right-line", "arrow-up-line",
	"attachment-line", "chat-1-line", "check-circle-line", "check-line", "close-circle-line",
	"close-line", "computer-line", "copy-2-line", "dashboard-2-line", "delete-2-line",
	"down-line", "download-2-line", "edit-line", "exit-line", "eye-close-line", "eye-line",
	"heartbeat-line", "history-line", "information-line", "key-2-line", "keyboard-line",
	"layers-line", "left-line", "list-check-line", "loading-3-line", "lock-line", "mic-line",
	"more-2-line", "pause-fill", "pin-line", "play-fill", "refresh-2-line", "right-line",
	"robot-line", "search-line", "settings-3-line", "sparkles-line", "stop-fill", "time-line",
	"translate-line", "up-line", "volume-line", "warning-line",
}

var pathRe = regexp.MustCompile(`<path fill="currentColor"[^>]*\sd="([^"]*)"|<path d="([^"]*)" fill="currentColor"`)

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var src struct {
		Icons map[string]struct{ Body string } `json:"icons"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		panic(err)
	}
	out := map[string][]string{}
	for _, n := range names {
		ic, ok := src.Icons[n]
		if !ok {
			panic("missing icon " + n)
		}
		for _, m := range pathRe.FindAllStringSubmatch(ic.Body, -1) {
			out[n] = append(out[n], m[1]+m[2])
		}
		if len(out[n]) == 0 {
			panic("no filled path in " + n)
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.Create("mingcute.json")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, "{")
	for i, k := range keys {
		v, _ := json.Marshal(out[k])
		sep := ","
		if i == len(keys)-1 {
			sep = ""
		}
		fmt.Fprintf(f, "%q: %s%s\n", k, v, sep)
	}
	fmt.Fprintln(f, "}")
}
