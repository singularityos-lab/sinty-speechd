package ssip

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/singularityos-lab/sinty-speechd/internal/engine"
	"github.com/singularityos-lab/sinty-speechd/internal/speech"
)

type Defaults func() (voice string, rate, pitch, volume float64)

type Server struct {
	Queue    *speech.Queue
	Engines  *engine.Set
	Defaults Defaults

	mu      sync.Mutex
	next    int
	clients map[int]*client
}

type client struct {
	id       int
	conn     net.Conn
	wmu      sync.Mutex
	name     string
	priority speech.Priority
	module   string
	language string
	voice    string
	rate     int
	pitch    int
	volume   int
	notify   map[string]bool
	jobs     map[uint32]bool
}

func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		s.mu.Lock()
		if s.clients == nil {
			s.clients = map[int]*client{}
		}
		s.next++
		c := &client{id: s.next, conn: conn, priority: speech.Text, volume: 100, notify: map[string]bool{}, jobs: map[uint32]bool{}}
		s.clients[c.id] = c
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (c *client) owner() string { return "ssip:" + strconv.Itoa(c.id) }

func (c *client) send(lines ...string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	c.conn.Write([]byte(b.String()))
}

func reply(code int, data []string, text string) []string {
	out := make([]string, 0, len(data)+1)
	for _, d := range data {
		out = append(out, fmt.Sprintf("%d-%s", code, d))
	}
	return append(out, fmt.Sprintf("%d %s", code, text))
}

func (s *Server) handle(c *client) {
	defer func() {
		c.conn.Close()
		s.Queue.Cancel(c.owner())
		s.mu.Lock()
		delete(s.clients, c.id)
		s.mu.Unlock()
	}()
	r := bufio.NewReader(c.conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		cmd := strings.ToUpper(f[0])
		switch cmd {
		case "SPEAK":
			c.send(reply(230, nil, "OK RECEIVING DATA")...)
			text, err := readData(r)
			if err != nil {
				return
			}
			c.send(s.speak(c, text)...)
		case "CHAR":
			arg := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
			if arg == "space" {
				arg = " "
			}
			c.send(s.speak(c, charName(arg))...)
		case "KEY":
			arg := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
			c.send(s.speak(c, keyName(arg))...)
		case "SOUND_ICON":
			c.send(reply(225, []string{"0"}, "OK MESSAGE QUEUED")...)
		case "STOP":
			s.each(c, f, func(o *client) { s.Queue.Stop(o.owner()) })
			c.send(reply(210, nil, "OK STOPPED")...)
		case "CANCEL":
			s.each(c, f, func(o *client) { s.Queue.Cancel(o.owner()) })
			c.send(reply(213, nil, "OK CANCELED")...)
		case "PAUSE":
			s.each(c, f, func(o *client) { s.Queue.Stop(o.owner()) })
			c.send(reply(211, nil, "OK PAUSED")...)
		case "RESUME":
			c.send(reply(212, nil, "OK RESUMED")...)
		case "SET":
			c.send(s.set(c, f)...)
		case "GET":
			c.send(s.get(c, f)...)
		case "LIST":
			c.send(s.list(c, f)...)
		case "HISTORY":
			if len(f) >= 3 && strings.EqualFold(f[1], "GET") && strings.EqualFold(f[2], "CLIENT_ID") {
				c.send(reply(245, []string{strconv.Itoa(c.id)}, "OK CLIENT ID SENT")...)
			} else {
				c.send(reply(410, nil, "ERR HISTORY NOT SUPPORTED")...)
			}
		case "BLOCK":
			if len(f) >= 2 && strings.EqualFold(f[1], "BEGIN") {
				c.send(reply(260, nil, "OK INSIDE BLOCK")...)
			} else {
				c.send(reply(261, nil, "OK OUTSIDE BLOCK")...)
			}
		case "QUIT":
			c.send(reply(231, nil, "HAPPY HACKING")...)
			return
		case "HELP":
			c.send(reply(248, []string{"SPEAK CHAR KEY STOP CANCEL SET GET LIST QUIT"}, "OK HELP SENT")...)
		default:
			c.send(reply(300, nil, "ERR UNKNOWN COMMAND")...)
		}
	}
}

func readData(r *bufio.Reader) (string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			return strings.Join(lines, "\n"), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		lines = append(lines, line)
	}
}

func (s *Server) each(c *client, f []string, do func(*client)) {
	target := "self"
	if len(f) >= 2 {
		target = strings.ToLower(f[1])
	}
	s.mu.Lock()
	var list []*client
	for _, o := range s.clients {
		if target == "all" || target == "self" && o == c || target == strconv.Itoa(o.id) {
			list = append(list, o)
		}
	}
	s.mu.Unlock()
	for _, o := range list {
		do(o)
	}
}

func scale(v int, span float64) float64 {
	return math.Pow(2, float64(v)/span)
}

func (s *Server) speak(c *client, text string) []string {
	voice, rate, pitch, volume := "", 1.0, 1.0, 1.0
	if s.Defaults != nil {
		voice, rate, pitch, volume = s.Defaults()
	}
	if c.voice != "" {
		voice = c.voice
	}
	language := c.language
	if language == "" {
		language = os.Getenv("LANG")
	}
	if c.module != "" && voice != "" && !strings.HasPrefix(voice, c.module+":") {
		voice = ""
	}
	id := s.Queue.Speak(speech.Request{
		Text:     text,
		Voice:    voice,
		Language: language,
		Rate:     rate * scale(c.rate, 50),
		Pitch:    pitch * scale(c.pitch, 100),
		Volume:   volume * float64(c.volume+100) / 200,
		Priority: c.priority,
		Owner:    c.owner(),
	})
	s.mu.Lock()
	c.jobs[id] = true
	s.mu.Unlock()
	return reply(225, []string{strconv.FormatUint(uint64(id), 10)}, "OK MESSAGE QUEUED")
}

func clamp(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	if err != nil || n < -100 || n > 100 {
		return 0, false
	}
	return n, true
}

func (s *Server) set(c *client, f []string) []string {
	if len(f) < 4 {
		return reply(410, nil, "ERR MISSING PARAMETER")
	}
	var targets []*client
	s.each(c, f, func(o *client) { targets = append(targets, o) })
	key := strings.ToUpper(f[2])
	value := strings.Join(f[3:], " ")
	ok := true
	for _, t := range targets {
		switch key {
		case "CLIENT_NAME":
			t.name = value
		case "PRIORITY":
			switch strings.ToLower(value) {
			case "important":
				t.priority = speech.Important
			case "message":
				t.priority = speech.Message
			default:
				t.priority = speech.Text
			}
		case "OUTPUT_MODULE":
			t.module = value
		case "LANGUAGE":
			t.language = value
		case "SYNTHESIS_VOICE":
			t.voice = s.voiceID(t.module, value)
		case "VOICE_TYPE":
		case "RATE":
			t.rate, ok = clamp(value)
		case "PITCH":
			t.pitch, ok = clamp(value)
		case "VOLUME":
			t.volume, ok = clamp(value)
		case "NOTIFICATION":
			if len(f) < 5 {
				return reply(410, nil, "ERR MISSING PARAMETER")
			}
			on := strings.EqualFold(f[4], "on")
			kind := strings.ToLower(f[3])
			if kind == "all" {
				for _, k := range []string{"begin", "end", "cancel", "pause", "resume", "index_marks"} {
					t.notify[k] = on
				}
			} else {
				t.notify[kind] = on
			}
		}
	}
	if !ok {
		return reply(410, nil, "ERR PARAMETER OUT OF RANGE")
	}
	return reply(203, nil, "OK "+key+" SET")
}

func (s *Server) voiceID(module, name string) string {
	for _, v := range s.Engines.Voices() {
		if (module == "" || v.Engine == module) && (v.Name == name || v.ID == name || strings.TrimPrefix(v.ID, v.Engine+":") == name) {
			return v.ID
		}
	}
	return name
}

func (s *Server) get(c *client, f []string) []string {
	if len(f) < 2 {
		return reply(410, nil, "ERR MISSING PARAMETER")
	}
	var v string
	switch strings.ToUpper(f[1]) {
	case "OUTPUT_MODULE":
		v = c.module
		if v == "" {
			if voices := s.Engines.Voices(); len(voices) > 0 {
				v = voices[0].Engine
			}
		}
	case "RATE":
		v = strconv.Itoa(c.rate)
	case "PITCH":
		v = strconv.Itoa(c.pitch)
	case "VOLUME":
		v = strconv.Itoa(c.volume)
	case "LANGUAGE":
		v = c.language
	case "VOICE_TYPE":
		v = "MALE1"
	default:
		return reply(410, nil, "ERR UNKNOWN PARAMETER")
	}
	return reply(251, []string{v}, "OK GET RETURNED")
}

func (s *Server) list(c *client, f []string) []string {
	if len(f) < 2 {
		return reply(410, nil, "ERR MISSING PARAMETER")
	}
	switch strings.ToUpper(f[1]) {
	case "OUTPUT_MODULES":
		var names []string
		seen := map[string]bool{}
		for _, v := range s.Engines.Voices() {
			if !seen[v.Engine] {
				seen[v.Engine] = true
				names = append(names, v.Engine)
			}
		}
		return reply(250, names, "OK MODULE LIST SENT")
	case "VOICES":
		return reply(249, []string{"MALE1", "MALE2", "MALE3", "FEMALE1", "FEMALE2", "FEMALE3", "CHILD_MALE", "CHILD_FEMALE"}, "OK VOICE LIST SENT")
	case "SYNTHESIS_VOICES":
		var rows []string
		for _, v := range s.Engines.Voices() {
			if c.module != "" && v.Engine != c.module {
				continue
			}
			if len(f) >= 3 && !strings.HasPrefix(strings.ToLower(v.Language), strings.ToLower(f[2])) {
				continue
			}
			rows = append(rows, strings.TrimPrefix(v.ID, v.Engine+":")+"\t"+v.Language+"\tnone")
		}
		return reply(249, rows, "OK VOICE LIST SENT")
	}
	return reply(410, nil, "ERR UNKNOWN PARAMETER")
}

func (s *Server) Event(e speech.Event) {
	if !strings.HasPrefix(e.Owner, "ssip:") {
		return
	}
	id, err := strconv.Atoi(e.Owner[5:])
	if err != nil {
		return
	}
	s.mu.Lock()
	c := s.clients[id]
	if c != nil && (e.Kind == speech.Finished || e.Kind == speech.Cancelled || e.Kind == speech.Failed) {
		delete(c.jobs, e.Job)
	}
	s.mu.Unlock()
	if c == nil {
		return
	}
	head := []string{strconv.FormatUint(uint64(e.Job), 10), strconv.Itoa(c.id)}
	switch e.Kind {
	case speech.Started:
		if c.notify["begin"] {
			c.send(reply(701, head, "BEGIN")...)
		}
	case speech.Marked:
		if c.notify["index_marks"] {
			c.send(reply(700, append(head, e.Mark), "INDEX MARK")...)
		}
	case speech.Finished:
		if c.notify["end"] {
			c.send(reply(702, head, "END")...)
		}
	case speech.Cancelled, speech.Failed:
		if c.notify["cancel"] {
			c.send(reply(703, head, "CANCELLED")...)
		}
	}
}

func charName(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	if name, ok := symbols[r]; ok && utf8.RuneCountInString(s) == 1 {
		return name
	}
	return s
}

func keyName(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if utf8.RuneCountInString(p) == 1 {
			parts[i] = charName(p)
		}
	}
	return strings.Join(parts, " ")
}

var symbols = map[rune]string{
	' ': "space", '.': "dot", ',': "comma", ';': "semicolon", ':': "colon",
	'!': "exclamation mark", '?': "question mark", '-': "dash", '_': "underscore",
	'/': "slash", '\\': "backslash", '(': "left paren", ')': "right paren",
	'[': "left bracket", ']': "right bracket", '{': "left brace", '}': "right brace",
	'<': "less than", '>': "greater than", '=': "equals", '+': "plus", '*': "star",
	'&': "and", '|': "bar", '@': "at", '#': "number", '$': "dollar", '%': "percent",
	'^': "caret", '~': "tilde", '`': "grave", '\'': "apostrophe", '"': "quote",
	'\t': "tab", '\n': "new line",
}
