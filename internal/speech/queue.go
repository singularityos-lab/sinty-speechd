package speech

import (
	"context"
	"errors"
	"sync"

	"github.com/singularityos-lab/sinty-speechd/internal/audio"
	"github.com/singularityos-lab/sinty-speechd/internal/engine"
)

type Priority int

const (
	Text Priority = iota
	Message
	Important
)

type Request struct {
	Text     string
	Voice    string
	Language string
	Rate     float64
	Pitch    float64
	Volume   float64
	Priority Priority
	Owner    string
}

type Kind int

const (
	Started Kind = iota
	Spoke
	Finished
	Cancelled
	Failed
	Marked
)

type Event struct {
	Job    uint32
	Owner  string
	Kind   Kind
	Offset int
	Length int
	Mark   string
	Err    error
}

type job struct {
	id  uint32
	req Request
}

type Queue struct {
	Engines *engine.Set
	Player  audio.Player
	Events  func(Event)

	mu      sync.Mutex
	next    uint32
	pending []*job
	current *job
	cancel  context.CancelFunc
	wake    chan struct{}
}

func NewQueue(engines *engine.Set, player audio.Player, events func(Event)) *Queue {
	q := &Queue{Engines: engines, Player: player, Events: events, wake: make(chan struct{}, 1)}
	return q
}

func (q *Queue) emit(e Event) {
	if q.Events != nil {
		q.Events(e)
	}
}

func (q *Queue) Speak(r Request) uint32 {
	q.mu.Lock()
	q.next++
	j := &job{id: q.next, req: r}
	var dropped []*job
	if r.Priority == Important {
		dropped = q.pending
		q.pending = nil
		if q.current != nil && q.current.req.Priority != Important && q.cancel != nil {
			q.cancel()
		}
	} else if r.Priority == Message {
		kept := q.pending[:0]
		for _, p := range q.pending {
			if p.req.Priority == Text {
				dropped = append(dropped, p)
			} else {
				kept = append(kept, p)
			}
		}
		q.pending = kept
		if q.current != nil && q.current.req.Priority == Text && q.cancel != nil {
			q.cancel()
		}
	}
	q.pending = append(q.pending, j)
	q.mu.Unlock()
	for _, d := range dropped {
		q.emit(Event{Job: d.id, Owner: d.req.Owner, Kind: Cancelled})
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return j.id
}

func (q *Queue) Stop(owner string) {
	q.mu.Lock()
	if q.current != nil && (owner == "" || q.current.req.Owner == owner) && q.cancel != nil {
		q.cancel()
	}
	q.mu.Unlock()
}

func (q *Queue) Cancel(owner string) {
	q.mu.Lock()
	var dropped []*job
	kept := q.pending[:0]
	for _, p := range q.pending {
		if owner == "" || p.req.Owner == owner {
			dropped = append(dropped, p)
		} else {
			kept = append(kept, p)
		}
	}
	q.pending = kept
	if q.current != nil && (owner == "" || q.current.req.Owner == owner) && q.cancel != nil {
		q.cancel()
	}
	q.mu.Unlock()
	for _, d := range dropped {
		q.emit(Event{Job: d.id, Owner: d.req.Owner, Kind: Cancelled})
	}
}

func (q *Queue) Speaking() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current != nil
}

func (q *Queue) take() *job {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	j := q.pending[0]
	q.pending = q.pending[1:]
	return j
}

func (q *Queue) Run(ctx context.Context) {
	for {
		j := q.take()
		if j == nil {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
				continue
			}
		}
		jctx, cancel := context.WithCancel(ctx)
		q.mu.Lock()
		q.current, q.cancel = j, cancel
		q.mu.Unlock()
		q.play(jctx, j)
		cancel()
		q.mu.Lock()
		q.current, q.cancel = nil, nil
		q.mu.Unlock()
	}
}

func (q *Queue) play(ctx context.Context, j *job) {
	r := j.req
	voice, eng, err := q.Engines.Pick(r.Voice, r.Language)
	if err == nil && eng == nil {
		err = errors.New("voice without an engine")
	}
	if err != nil {
		q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Failed, Err: err})
		return
	}
	q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Started})
	volume := r.Volume
	if volume == 0 {
		volume = 1
	}
	params := engine.Params{Rate: r.Rate, Pitch: r.Pitch}
	sentences := Pieces(r.Text, 400)
	type result struct {
		a   engine.Audio
		err error
	}
	ahead := make(chan result, 1)
	go func() {
		defer close(ahead)
		for _, s := range sentences {
			if s.Mark != "" {
				continue
			}
			a, err := eng.Synthesize(ctx, voice, s.Text, params)
			select {
			case ahead <- result{a, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for _, s := range sentences {
		if s.Mark != "" {
			if ctx.Err() == nil {
				q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Marked, Mark: s.Mark, Offset: s.Offset})
			}
			continue
		}
		res, ok := <-ahead
		if !ok || ctx.Err() != nil {
			q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Cancelled})
			return
		}
		if res.err != nil {
			q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Failed, Err: res.err})
			return
		}
		q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Spoke, Offset: s.Offset, Length: len([]rune(s.Text))})
		if err := q.Player.Play(ctx, audio.Gain(res.a.PCM, volume), res.a.SampleRate); err != nil {
			if ctx.Err() != nil {
				q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Cancelled})
			} else {
				q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Failed, Err: err})
			}
			return
		}
	}
	if ctx.Err() != nil {
		q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Cancelled})
		return
	}
	q.emit(Event{Job: j.id, Owner: r.Owner, Kind: Finished})
}
