package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/singularityos-lab/sinty-speechd/internal/catalog"
	"github.com/singularityos-lab/sinty-speechd/internal/engine"
	"github.com/singularityos-lab/sinty-speechd/internal/speech"
)

const (
	Name  = "dev.sinty.Speech1"
	Path  = dbus.ObjectPath("/dev/sinty/Speech1")
	Iface = "dev.sinty.Speech1"
)

type Settings struct {
	Voice  string  `json:"voice"`
	Rate   float64 `json:"rate"`
	Pitch  float64 `json:"pitch"`
	Volume float64 `json:"volume"`
}

func DefaultSettings() Settings {
	return Settings{Rate: 1, Pitch: 1, Volume: 1}
}

func LoadSettings(path string) Settings {
	s := DefaultSettings()
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &s)
	}
	if s.Rate <= 0 {
		s.Rate = 1
	}
	if s.Pitch <= 0 {
		s.Pitch = 1
	}
	if s.Volume < 0 || s.Volume > 2 {
		s.Volume = 1
	}
	return s
}

type Service struct {
	conn     *dbus.Conn
	queue    *speech.Queue
	engines  *engine.Set
	path     string
	props    *prop.Properties
	mu       sync.Mutex
	settings Settings
	catalog  *catalog.Catalog
	neural   bool
	installs map[string]context.CancelFunc
}

func New(conn *dbus.Conn, queue *speech.Queue, engines *engine.Set, settingsPath string, voices *catalog.Catalog, neural bool) (*Service, error) {
	s := &Service{conn: conn, queue: queue, engines: engines, path: settingsPath, settings: LoadSettings(settingsPath), catalog: voices, neural: neural, installs: map[string]context.CancelFunc{}}
	if err := conn.Export(s, Path, Iface); err != nil {
		return nil, err
	}
	writable := func(apply func(*Settings, interface{}) bool) func(*prop.Change) *dbus.Error {
		return func(c *prop.Change) *dbus.Error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !apply(&s.settings, c.Value) {
				return dbus.MakeFailedError(errInvalid)
			}
			s.save()
			return nil
		}
	}
	number := func(min, max float64, set func(*Settings, float64)) func(*Settings, interface{}) bool {
		return func(st *Settings, v interface{}) bool {
			f, ok := v.(float64)
			if !ok || f < min || f > max {
				return false
			}
			set(st, f)
			return true
		}
	}
	props, err := prop.Export(conn, Path, prop.Map{Iface: {
		"Voice": {Value: s.settings.Voice, Writable: true, Emit: prop.EmitTrue, Callback: writable(func(st *Settings, v interface{}) bool {
			id, ok := v.(string)
			if ok {
				st.Voice = id
			}
			return ok
		})},
		"Rate":         {Value: s.settings.Rate, Writable: true, Emit: prop.EmitTrue, Callback: writable(number(0.25, 4, func(st *Settings, f float64) { st.Rate = f }))},
		"Pitch":        {Value: s.settings.Pitch, Writable: true, Emit: prop.EmitTrue, Callback: writable(number(0.25, 2, func(st *Settings, f float64) { st.Pitch = f }))},
		"Volume":       {Value: s.settings.Volume, Writable: true, Emit: prop.EmitTrue, Callback: writable(number(0, 2, func(st *Settings, f float64) { st.Volume = f }))},
		"Speaking":     {Value: false, Emit: prop.EmitTrue},
		"Paused":       {Value: false, Emit: prop.EmitTrue},
		"NeuralVoices": {Value: neural, Emit: prop.EmitFalse},
	}})
	if err != nil {
		return nil, err
	}
	s.props = props
	node := &introspect.Node{
		Name: string(Path),
		Interfaces: []introspect.Interface{introspect.IntrospectData, prop.IntrospectData, {
			Name:       Iface,
			Methods:    introspect.Methods(s),
			Properties: props.Introspection(Iface),
			Signals: []introspect.Signal{
				{Name: "Started", Args: []introspect.Arg{{Name: "job", Type: "u"}}},
				{Name: "Sentence", Args: []introspect.Arg{{Name: "job", Type: "u"}, {Name: "offset", Type: "i"}, {Name: "length", Type: "i"}}},
				{Name: "Word", Args: []introspect.Arg{{Name: "job", Type: "u"}, {Name: "offset", Type: "i"}, {Name: "length", Type: "i"}}},
				{Name: "Finished", Args: []introspect.Arg{{Name: "job", Type: "u"}, {Name: "completed", Type: "b"}}},
				{Name: "VoiceProgress", Args: []introspect.Arg{{Name: "key", Type: "s"}, {Name: "done", Type: "x"}, {Name: "total", Type: "x"}}},
				{Name: "VoiceInstalled", Args: []introspect.Arg{{Name: "key", Type: "s"}, {Name: "ok", Type: "b"}, {Name: "error", Type: "s"}}},
			},
		}},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), Path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}
	return s, nil
}

type invalid struct{}

func (invalid) Error() string { return "value out of range" }

var errInvalid = invalid{}

func (s *Service) save() {
	if s.path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(s.path), 0o755)
	data, _ := json.MarshalIndent(s.settings, "", "  ")
	tmp := s.path + ".new"
	if os.WriteFile(tmp, append(data, '\n'), 0o644) == nil {
		os.Rename(tmp, s.path)
	}
}

func (s *Service) Current() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Service) Event(e speech.Event) {
	switch e.Kind {
	case speech.Started:
		s.props.SetMust(Iface, "Speaking", true)
	case speech.Paused:
		s.props.SetMust(Iface, "Paused", true)
	case speech.Resumed:
		s.props.SetMust(Iface, "Paused", false)
	case speech.Finished, speech.Cancelled, speech.Failed:
		s.props.SetMust(Iface, "Speaking", false)
		s.props.SetMust(Iface, "Paused", false)
	}
	if e.Owner == "" || e.Owner[0] != ':' {
		return
	}
	switch e.Kind {
	case speech.Started:
		s.conn.Emit(Path, Iface+".Started", e.Job)
	case speech.Spoke:
		s.conn.Emit(Path, Iface+".Sentence", e.Job, int32(e.Offset), int32(e.Length))
	case speech.Word:
		s.conn.Emit(Path, Iface+".Word", e.Job, int32(e.Offset), int32(e.Length))
	case speech.Finished, speech.Cancelled, speech.Failed:
		s.conn.Emit(Path, Iface+".Finished", e.Job, e.Kind == speech.Finished)
	}
}

func (s *Service) request(sender dbus.Sender, text, voice string, rate, pitch float64, priority speech.Priority) speech.Request {
	st := s.Current()
	if voice == "" {
		voice = st.Voice
	}
	if rate <= 0 {
		rate = 1
	}
	if pitch <= 0 {
		pitch = 1
	}
	return speech.Request{Text: text, Voice: voice, Language: os.Getenv("LANG"), Rate: rate * st.Rate, Pitch: pitch * st.Pitch, Volume: st.Volume, Priority: priority, Owner: string(sender)}
}

func (s *Service) Speak(sender dbus.Sender, text, voice string, rate, pitch float64) (uint32, *dbus.Error) {
	return s.queue.Speak(s.request(sender, text, voice, rate, pitch, speech.Text)), nil
}

func (s *Service) Announce(sender dbus.Sender, text string) (uint32, *dbus.Error) {
	return s.queue.Speak(s.request(sender, text, "", 1, 1, speech.Important)), nil
}

func (s *Service) Stop(sender dbus.Sender) *dbus.Error {
	s.queue.Cancel(string(sender))
	return nil
}

func (s *Service) Pause() *dbus.Error {
	s.queue.Pause("")
	return nil
}

func (s *Service) Resume() *dbus.Error {
	s.queue.Resume("")
	return nil
}

type available struct {
	Key       string
	Name      string
	Language  string
	Quality   string
	Size      int64
	Installed bool
}

func (s *Service) AvailableVoices(language string) ([]available, *dbus.Error) {
	if s.catalog == nil || !s.neural {
		return []available{}, nil
	}
	if language == "" {
		language = os.Getenv("LANG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	list, err := s.catalog.Voices(ctx, language)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	out := []available{}
	for _, v := range list {
		out = append(out, available{v.Key, v.Name, v.Language, v.Quality, v.Size, s.catalog.Installed(v.Key)})
	}
	return out, nil
}

func (s *Service) InstallVoice(key string) *dbus.Error {
	if s.catalog == nil || !s.neural {
		return dbus.NewError(Iface+".Error.NotSupported", []interface{}{"neural voices are not available"})
	}
	s.mu.Lock()
	if _, busy := s.installs[key]; busy {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.installs[key] = cancel
	s.mu.Unlock()
	go func() {
		var last time.Time
		err := s.catalog.Install(ctx, key, func(done, total int64) {
			if time.Since(last) > 200*time.Millisecond || done == total {
				last = time.Now()
				s.conn.Emit(Path, Iface+".VoiceProgress", key, done, total)
			}
		})
		s.mu.Lock()
		delete(s.installs, key)
		s.mu.Unlock()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		s.conn.Emit(Path, Iface+".VoiceInstalled", key, err == nil, msg)
	}()
	return nil
}

func (s *Service) CancelInstall(key string) *dbus.Error {
	s.mu.Lock()
	if cancel, ok := s.installs[key]; ok {
		cancel()
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) RemoveVoice(key string) *dbus.Error {
	if s.catalog == nil {
		return dbus.NewError(Iface+".Error.NotSupported", []interface{}{"neural voices are not available"})
	}
	if err := s.catalog.Remove(key); err != nil {
		return dbus.MakeFailedError(err)
	}
	s.mu.Lock()
	if s.settings.Voice == "piper:"+key {
		s.settings.Voice = ""
		s.save()
		s.props.SetMust(Iface, "Voice", "")
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) ListVoices() ([][]string, *dbus.Error) {
	var out [][]string
	for _, v := range s.engines.Voices() {
		out = append(out, []string{v.ID, v.Name, v.Language, v.Engine})
	}
	return out, nil
}

func (s *Service) Preview(sender dbus.Sender, voice string) (uint32, *dbus.Error) {
	v, _, err := s.engines.Pick(voice, os.Getenv("LANG"))
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	r := s.request(sender, sample(v.Language), v.ID, 1, 1, speech.Important)
	return s.queue.Speak(r), nil
}

func sample(language string) string {
	switch {
	case len(language) >= 2 && language[:2] == "it":
		return "Ciao, questa è la mia voce."
	case len(language) >= 2 && language[:2] == "de":
		return "Hallo, so klingt meine Stimme."
	case len(language) >= 2 && language[:2] == "fr":
		return "Bonjour, voici ma voix."
	case len(language) >= 2 && language[:2] == "es":
		return "Hola, así suena mi voz."
	}
	return "Hello, this is how I sound."
}
