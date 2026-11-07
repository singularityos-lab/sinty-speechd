package ssip

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-speechd/internal/engine"
	"github.com/singularityos-lab/sinty-speechd/internal/speech"
)

type fake struct{}

func (fake) Name() string { return "espeak-ng" }
func (fake) Voices() []engine.Voice {
	return []engine.Voice{{ID: "espeak-ng:it", Name: "Italian", Language: "it", Engine: "espeak-ng"}, {ID: "espeak-ng:en-us", Name: "English", Language: "en-US", Engine: "espeak-ng"}}
}
func (fake) Synthesize(_ context.Context, v engine.Voice, text string, p engine.Params) (engine.Audio, error) {
	return engine.Audio{PCM: []byte(v.ID + "|" + text), SampleRate: 16000}, nil
}

type player struct {
	mu     sync.Mutex
	played []string
}

func (p *player) Play(_ context.Context, pcm []byte, _ int) error {
	p.mu.Lock()
	p.played = append(p.played, string(pcm))
	p.mu.Unlock()
	return nil
}

type session struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func (s *session) line() string {
	s.t.Helper()
	s.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	l, err := s.r.ReadString('\n')
	if err != nil {
		s.t.Fatal(err)
	}
	if !strings.HasSuffix(l, "\r\n") {
		s.t.Fatalf("line without CRLF: %q", l)
	}
	return strings.TrimSuffix(l, "\r\n")
}

func (s *session) reply() []string {
	var out []string
	for {
		l := s.line()
		out = append(out, l)
		if len(l) >= 4 && l[3] == ' ' {
			return out
		}
	}
}

func (s *session) do(cmd string) []string {
	s.t.Helper()
	s.c.Write([]byte(cmd + "\r\n"))
	return s.reply()
}

func open(t *testing.T) (*session, *player) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &player{}
	engines := &engine.Set{Engines: []engine.Engine{fake{}}}
	srv := &Server{Engines: engines}
	srv.Queue = speech.NewQueue(engines, p, srv.Event)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Queue.Run(ctx)
	go srv.Serve(l)
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &session{t: t, c: c, r: bufio.NewReader(c)}, p
}

func TestOrcaStyleSessionSpeaksAndNotifies(t *testing.T) {
	s, p := open(t)
	for _, cmd := range []string{"SET self CLIENT_NAME user:orca:main", "SET self NOTIFICATION ALL on", "SET self OUTPUT_MODULE espeak-ng", "SET self LANGUAGE it", "SET self RATE 20", "SET self PRIORITY message"} {
		if r := s.do(cmd); !strings.HasPrefix(r[len(r)-1], "2") {
			t.Fatalf("%s: %q", cmd, r)
		}
	}
	if r := s.do("SET self RATE 400"); !strings.HasPrefix(r[0], "410") {
		t.Fatalf("out of range rate accepted: %q", r)
	}
	if r := s.do("HISTORY GET CLIENT_ID"); len(r) != 2 || !strings.HasPrefix(r[0], "245-") {
		t.Fatalf("%q", r)
	}
	if r := s.do("LIST OUTPUT_MODULES"); strings.Join(r, ",") != "250-espeak-ng,250 OK MODULE LIST SENT" {
		t.Fatalf("%q", r)
	}
	if r := s.do("LIST SYNTHESIS_VOICES"); len(r) != 3 || r[0] != "249-it\tit\tnone" {
		t.Fatalf("%q", r)
	}
	if r := s.do("SPEAK"); r[0] != "230 OK RECEIVING DATA" {
		t.Fatalf("%q", r)
	}
	s.c.Write([]byte("<speak>Ciao<mark name=\"m1\"/> mondo.</speak>\r\n..punto\r\n.\r\n"))
	r := s.reply()
	if len(r) != 2 || r[1] != "225 OK MESSAGE QUEUED" {
		t.Fatalf("%q", r)
	}
	id := strings.TrimPrefix(r[0], "225-")
	var events []string
	for len(events) == 0 || events[len(events)-1] != "702 END" {
		l := s.line()
		if strings.HasPrefix(l, "7") && l[3] == ' ' {
			events = append(events, l)
		}
		if l == "700-m1" {
			events = append(events, "mark")
		}
		if strings.HasPrefix(l, "701-") && l != "701-"+id && !strings.HasPrefix(l, "701-1") {
			t.Fatalf("begin for another message: %q", l)
		}
	}
	if strings.Join(events, ",") != "701 BEGIN,mark,700 INDEX MARK,702 END" {
		t.Fatalf("%q", events)
	}
	p.mu.Lock()
	played := strings.Join(p.played, ",")
	p.mu.Unlock()
	if played != "espeak-ng:it|Ciao,espeak-ng:it|mondo.,espeak-ng:it|.punto" {
		t.Fatalf("%q", played)
	}
	if r := s.do("CHAR ?"); r[1] != "225 OK MESSAGE QUEUED" {
		t.Fatalf("%q", r)
	}
	for s.line() != "702 END" {
	}
	if r := s.do("CANCEL self"); r[0] != "213 OK CANCELED" {
		t.Fatalf("%q", r)
	}
	if r := s.do("QUIT"); r[0] != "231 HAPPY HACKING" {
		t.Fatalf("%q", r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.played[len(p.played)-1] != "espeak-ng:it|question mark" {
		t.Fatalf("%q", p.played)
	}
}

func TestUnknownCommandsAreRefused(t *testing.T) {
	s, _ := open(t)
	if r := s.do("FROBNICATE"); !strings.HasPrefix(r[0], "300") {
		t.Fatalf("%q", r)
	}
	if r := s.do("SET self"); !strings.HasPrefix(r[0], "410") {
		t.Fatalf("%q", r)
	}
}
