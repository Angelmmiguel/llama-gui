// Package logs turns llama-server output into structured lines and derived events.
// It is pure parsing: no I/O, and no input makes it panic.
package logs

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Levels and sources a Line can carry.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelDebug = "debug"

	SourceSrv    = "srv"
	SourceSlot   = "slot"
	SourceRouter = "router"
	SourceGGML   = "ggml"
	SourceCmn    = "cmn"
	SourceOther  = "other"
)

// Line is one parsed output line.
type Line struct {
	Time    time.Time // arrival: upstream's own clock is relative
	Level   string
	Source  string
	Port    int // 0 for the router itself
	Model   string
	Message string
	Raw     string
	Slot    int
	HasSlot bool
	Task    int
	HasTask bool
}

// Parser parses output lines. It holds no state.
type Parser struct{}

var (
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	portRe   = regexp.MustCompile(`^\[\s*(\d+)\] ?`)
	prefixRe = regexp.MustCompile(`^(?:\d+(?:\.\d+)+ )?([IWEDV]) `)
	slotRe   = regexp.MustCompile(`\bid\s+(\d+) \| task (-?\d+)`)
	slotIDRe = regexp.MustCompile(`\bid\s+(\d+)\b`)
	routerRe = regexp.MustCompile(`instance|measure|Available models`)
)

type jsonLine struct {
	Type  string `json:"type"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// Parse parses one raw output line stamped with its arrival time. portToModel
// maps a child's port to its model id and may be nil.
func (Parser) Parse(raw string, arrival time.Time, portToModel func(int) string) Line {
	l := Line{Time: arrival, Raw: raw}
	s := strings.TrimRight(ansiRe.ReplaceAllString(raw, ""), "\r\n")
	p := unwrap(s)
	l.Level, l.Port, l.Message = p.level, p.port, p.msg
	if p.port > 0 && portToModel != nil {
		l.Model = portToModel(p.port)
	}
	if p.nonLog {
		l.Source = SourceOther
		return l
	}
	l.Source = source(l.Message, p.port, p.child)
	if l.Source != SourceSlot {
		return l
	}
	if m := slotRe.FindStringSubmatch(l.Message); m != nil {
		l.Slot, l.HasSlot = atoi(m[1]), true
		if t := atoi(m[2]); t >= 0 {
			l.Task, l.HasTask = t, true
		}
	} else if m := slotIDRe.FindStringSubmatch(l.Message); m != nil {
		l.Slot, l.HasSlot = atoi(m[1]), true
	}
	return l
}

type unwrapped struct {
	level  string
	msg    string
	port   int
	child  bool // forwarded from a child process
	nonLog bool // JSON record whose type is not "log"
}

func unwrap(s string) unwrapped {
	if j, ok := parseJSON(s); ok {
		if j.Type != "log" {
			return unwrapped{level: LevelDebug, msg: s, nonLog: true}
		}
		if j.Level == "none" {
			if m := portRe.FindStringSubmatch(j.Msg); m != nil {
				inner := unwrap(strings.TrimRight(j.Msg[len(m[0]):], "\r\n"))
				inner.port, inner.child = atoi(m[1]), true
				return inner
			}
		}
		return unwrapped{level: normLevel(j.Level), msg: strings.TrimRight(j.Msg, "\r\n")}
	}
	u := unwrapped{level: LevelInfo, msg: s}
	if m := portRe.FindStringSubmatch(u.msg); m != nil {
		u.port, u.child = atoi(m[1]), true
		u.msg = u.msg[len(m[0]):]
	}
	if m := prefixRe.FindStringSubmatch(u.msg); m != nil {
		u.level = map[string]string{"I": LevelInfo, "W": LevelWarn, "E": LevelError, "D": LevelDebug, "V": LevelDebug}[m[1]]
		u.msg = u.msg[len(m[0]):]
	}
	return u
}

func parseJSON(s string) (j jsonLine, ok bool) {
	if !strings.HasPrefix(s, "{") || json.Unmarshal([]byte(s), &j) != nil {
		return j, false
	}
	return j, j.Type != ""
}

func normLevel(l string) string {
	switch strings.ToLower(l) {
	case "warn", "warning":
		return LevelWarn
	case "error", "err", "fatal", "critical":
		return LevelError
	case "debug", "trace":
		return LevelDebug
	}
	return LevelInfo
}

var ggmlPrefixes = []string{"llama_", "load_", "ggml_", "sched_", "print_info", "clip_", "mtmd"}

func source(msg string, port int, child bool) string {
	word, _, _ := strings.Cut(strings.TrimSpace(msg), " ")
	word = strings.TrimSuffix(word, ":")
	switch word {
	case "srv":
		if !child && port == 0 && routerRe.MatchString(msg) {
			return SourceRouter
		}
		return SourceSrv
	case "slot":
		return SourceSlot
	case "cmn":
		return SourceCmn
	}
	for _, pre := range ggmlPrefixes {
		if strings.HasPrefix(word, pre) {
			return SourceGGML
		}
	}
	return SourceOther
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
