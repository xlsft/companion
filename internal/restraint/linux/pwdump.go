//go:build linux

// Package linux provides restraint signals for Linux desktops. It only reads
// state (pw-dump, D-Bus properties, niri IPC) and never creates inhibitors.
package linux

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	classAudioIn   = "Stream/Input/Audio"
	classVideoIn   = "Stream/Input/Video"
	classVideoOut  = "Stream/Output/Video"
	classVideoSrc  = "Video/Source"
	classAudioSrc  = "Audio/Source"
	rolePrefixCam  = "camera"
	roleScreen     = "screen"
	roleComm       = "communication"
	stateRunning   = "running"
	pwNodeType     = "PipeWire:Interface:Node"
	pwLinkType     = "PipeWire:Interface:Link"
	pwClientType   = "PipeWire:Interface:Client"
	screencastHint = "screen" // substring of media.name/node.name of portal screencast streams
)

// node is the subset of a PipeWire node we care about.
type node struct {
	ID          int
	Class       string
	Role        string
	Name        string // node.name
	MediaName   string
	App         string // application.name
	Binary      string // application.process.binary
	PID         int
	State       string
	CaptureSink bool // stream.capture.sink: records a sink monitor, not a microphone
	ClientID    int
}

type graph struct {
	nodes map[int]*node
	// upstream[inputNode] = output nodes feeding it.
	upstream map[int][]int
}

type rawObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		State        string                     `json:"state"`
		OutputNodeID int                        `json:"output-node-id"`
		InputNodeID  int                        `json:"input-node-id"`
		Props        map[string]json.RawMessage `json:"props"`
	} `json:"info"`
}

// parsePWDump reads `pw-dump` output.
func parsePWDump(data []byte) (*graph, error) {
	var objs []rawObject
	if err := json.Unmarshal(data, &objs); err != nil {
		return nil, fmt.Errorf("parse pw-dump: %w", err)
	}
	g := &graph{nodes: map[int]*node{}, upstream: map[int][]int{}}
	clientPID := map[int]int{}
	clientApp := map[int]string{}
	for _, o := range objs {
		if o.Type == pwClientType {
			clientPID[o.ID] = propInt(o.Info.Props, "application.process.id")
			clientApp[o.ID] = propStr(o.Info.Props, "application.name")
		}
	}
	for _, o := range objs {
		switch o.Type {
		case pwNodeType:
			p := o.Info.Props
			n := &node{
				ID:          o.ID,
				Class:       propStr(p, "media.class"),
				Role:        strings.ToLower(propStr(p, "media.role")),
				Name:        propStr(p, "node.name"),
				MediaName:   propStr(p, "media.name"),
				App:         propStr(p, "application.name"),
				Binary:      propStr(p, "application.process.binary"),
				PID:         propInt(p, "application.process.id"),
				State:       o.Info.State,
				CaptureSink: propBool(p, "stream.capture.sink"),
				ClientID:    propInt(p, "client.id"),
			}
			if n.PID == 0 {
				n.PID = clientPID[n.ClientID]
			}
			if n.App == "" {
				n.App = clientApp[n.ClientID]
			}
			g.nodes[o.ID] = n
		case pwLinkType:
			g.upstream[o.Info.InputNodeID] = append(g.upstream[o.Info.InputNodeID], o.Info.OutputNodeID)
		}
	}
	return g, nil
}

// Consumer describes a process using a stream; Detail is a technical label
// (app name or binary), never content.
type Consumer struct {
	PID    int
	Detail string
}

func (n *node) label() string {
	for _, s := range []string{n.App, n.Binary, n.Name} {
		if s != "" {
			return s
		}
	}
	return "unknown"
}

// screenShares returns running video input streams that consume a screencast
// source. Cameras are excluded. ownPID is skipped (our own capture is not a share).
func (g *graph) screenShares(ownPID int) []Consumer {
	var out []Consumer
	for _, n := range g.nodes {
		if n.Class != classVideoIn || n.State != stateRunning || n.PID == ownPID && ownPID != 0 {
			continue
		}
		if strings.HasPrefix(n.Role, rolePrefixCam) {
			continue
		}
		if n.Role == roleScreen || g.fedByScreencast(n.ID) {
			out = append(out, Consumer{PID: n.PID, Detail: n.label()})
		}
	}
	sortConsumers(out)
	return out
}

func (g *graph) fedByScreencast(id int) bool {
	for _, up := range g.upstream[id] {
		s := g.nodes[up]
		if s == nil || (s.Class != classVideoOut && s.Class != classVideoSrc) {
			continue
		}
		if strings.HasPrefix(s.Role, rolePrefixCam) {
			continue
		}
		if s.Role == roleScreen {
			return true
		}
		// xdg-desktop-portal backends publish the stream under their own name.
		hay := strings.ToLower(s.Name + " " + s.MediaName + " " + s.App)
		if strings.Contains(hay, screencastHint) || strings.Contains(hay, "xdg-desktop-portal") {
			return true
		}
	}
	return false
}

// callApps are matched case-insensitively against app name / binary / node name.
var callApps = []string{
	"zoom", "teams", "telegram", "discord", "slack", "skype", "webex", "jitsi",
	"signal", "element", "meet", "whatsapp", "linphone",
	// Browsers: a live mic capture there is almost always WebRTC.
	"firefox", "chromium", "chrome", "zen", "brave", "vivaldi", "librewolf",
}

func isCallApp(n *node) bool {
	hay := strings.ToLower(n.App + " " + n.Binary + " " + n.Name)
	for _, a := range callApps {
		if strings.Contains(hay, a) {
			return true
		}
	}
	return false
}

// micUsers returns running audio capture streams fed by a real microphone
// (Audio/Source) from other processes. With anyApp false only well-known call
// apps and streams with the Communication role count, so a dictation tool or
// a recorder does not mute notifications.
func (g *graph) micUsers(ownPID int, anyApp bool) []Consumer {
	var out []Consumer
	for _, n := range g.nodes {
		if n.Class != classAudioIn || n.State != stateRunning || n.CaptureSink || n.PID == ownPID && ownPID != 0 {
			continue
		}
		if !g.fedByMic(n.ID) {
			continue
		}
		if anyApp || n.Role == roleComm || isCallApp(n) {
			out = append(out, Consumer{PID: n.PID, Detail: n.label()})
		}
	}
	sortConsumers(out)
	return out
}

func (g *graph) fedByMic(id int) bool {
	for _, up := range g.upstream[id] {
		if s := g.nodes[up]; s != nil && strings.HasPrefix(s.Class, classAudioSrc) {
			return true
		}
	}
	return false
}

func sortConsumers(c []Consumer) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].PID != c[j].PID {
			return c[i].PID < c[j].PID
		}
		return c[i].Detail < c[j].Detail
	})
}

func propStr(p map[string]json.RawMessage, k string) string {
	var s string
	if json.Unmarshal(p[k], &s) == nil {
		return s
	}
	return ""
}

func propInt(p map[string]json.RawMessage, k string) int {
	var n int
	if json.Unmarshal(p[k], &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(p[k], &s) == nil {
		_, _ = fmt.Sscanf(s, "%d", &n) // not a number: 0
	}
	return n
}

func propBool(p map[string]json.RawMessage, k string) bool {
	var b bool
	if json.Unmarshal(p[k], &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(p[k], &s) == nil && s == "true"
}
