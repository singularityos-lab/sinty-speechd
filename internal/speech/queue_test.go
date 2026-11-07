package speech

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-speechd/internal/engine"
)

type fake struct{}

func (fake) Name() string { return "fake" }
func (fake) Voices() []engine.Voice {
	return []engine.Voice{{ID: "fake:it", Language: "it", Engine: "fake"}}
}
func (fake) Synthesize(_ context.Context, _ engine.Voice, text string, _ engine.Params) (engine.Audio, error) {
	return engine.Audio{PCM: []byte(text), SampleRate: 16000}, nil
}

type player struct {
	mu     sync.Mutex
	played []string
	hold   chan struct{}
}

func (p *player) Play(ctx context.Context, pcm []byte, rate int) error {
	p.mu.Lock()
	p.played = append(p.played, string(pcm))
	p.mu.Unlock()
	if p.hold != nil {
		select {
		case <-p.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type recorder struct {
	mu     sync.Mutex
	events []Event
	ch     chan Event
}

func (r *recorder) add(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	r.ch <- e
}

func (r *recorder) until(t *testing.T, job uint32, kind Kind) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-r.ch:
			if e.Job == job && e.Kind == kind {
				return
			}
		case <-timeout:
			t.Fatalf("job %d never reached %d: %+v", job, kind, r.events)
		}
	}
}

func start(t *testing.T, p *player) (*Queue, *recorder) {
	rec := &recorder{ch: make(chan Event, 64)}
	q := NewQueue(&engine.Set{Engines: []engine.Engine{fake{}}}, p, rec.add)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Run(ctx)
	return q, rec
}

func TestQueueSpeaksSentencesInOrderWithEvents(t *testing.T) {
	p := &player{}
	q, rec := start(t, p)
	id := q.Speak(Request{Text: `Uno. Due.<mark name="m"/> Tre.`, Owner: "a"})
	rec.until(t, id, Finished)
	if strings.Join(p.played, "|") != "Uno.|Due.|Tre." {
		t.Fatalf("%q", p.played)
	}
	var kinds []Kind
	for _, e := range rec.events {
		kinds = append(kinds, e.Kind)
	}
	want := []Kind{Started, Spoke, Spoke, Marked, Spoke, Finished}
	if len(kinds) != len(want) {
		t.Fatalf("%v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("%v", kinds)
		}
	}
}

func TestImportantInterruptsTextAndDropsTheQueue(t *testing.T) {
	p := &player{hold: make(chan struct{})}
	q, rec := start(t, p)
	first := q.Speak(Request{Text: "Lungo testo.", Owner: "a"})
	rec.until(t, first, Spoke)
	second := q.Speak(Request{Text: "In coda.", Owner: "a"})
	urgent := q.Speak(Request{Text: "Batteria scarica.", Priority: Important, Owner: "b"})
	rec.until(t, first, Cancelled)
	close(p.hold)
	rec.until(t, urgent, Finished)
	for _, e := range rec.events {
		if e.Job == second && e.Kind != Cancelled {
			t.Fatalf("the queued text was spoken: %+v", rec.events)
		}
	}
}

func TestCancelOnlyTouchesItsOwner(t *testing.T) {
	p := &player{hold: make(chan struct{})}
	q, rec := start(t, p)
	a := q.Speak(Request{Text: "Primo.", Owner: "a"})
	rec.until(t, a, Spoke)
	b := q.Speak(Request{Text: "Secondo.", Owner: "b"})
	q.Cancel("b")
	rec.until(t, b, Cancelled)
	if !q.Speaking() {
		t.Fatal("owner a was stopped")
	}
	q.Cancel("a")
	rec.until(t, a, Cancelled)
}
