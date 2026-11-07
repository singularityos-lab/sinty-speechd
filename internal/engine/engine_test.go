package engine

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

const voiceList = `Pty Language       Age/Gender VoiceName          File                 Other Languages
 5  af              --/M      Afrikaans          gmw/af
 5  en-gb           --/M      English_(Great_Britain) gmw/en
 2  en-us           --/M      English_(America)  gmw/en-US
 5  it              --/M      Italian            roa/it
 5  it              --/M      Italian_alt        roa/it
`

func TestEspeakVoicesAreOnePerLanguage(t *testing.T) {
	v := parseEspeakVoices([]byte(voiceList))
	if len(v) != 4 {
		t.Fatalf("%+v", v)
	}
	if v[1].ID != "espeak-ng:en-gb" || v[1].Name != "English (Great Britain)" || v[3].Language != "it" {
		t.Fatalf("%+v", v)
	}
}

func wav(rate int, samples []int16) []byte {
	data := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(s))
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 0xffffffff)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], 0xffffffff)
	return append(h, data...)
}

func TestParseWAVHandlesStreamedLengths(t *testing.T) {
	a, err := ParseWAV(wav(22050, []int16{1, -2, 3}))
	if err != nil || a.SampleRate != 22050 || len(a.PCM) != 6 {
		t.Fatalf("%v %+v", err, a)
	}
	if _, err := ParseWAV([]byte("RIFF....WAVEdata")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPiperVoicesComeFromModelsWithConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "it_IT-paola-medium.onnx"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "it_IT-paola-medium.onnx.json"), []byte(`{"dataset":"paola","language":{"code":"it_IT"},"audio":{"sample_rate":22050}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.onnx"), nil, 0o644)
	p := &Piper{Binary: "/bin/true", Dirs: []string{dir, dir}}
	v := p.Voices()
	if len(v) != 1 || v[0].ID != "piper:it_IT-paola-medium" || v[0].Language != "it-IT" || v[0].Name != "paola" {
		t.Fatalf("%+v", v)
	}
	if (&Piper{Dirs: []string{dir}}).Voices() != nil {
		t.Fatal("voices without the piper binary")
	}
}

type listed struct{ voices []Voice }

func (l *listed) Name() string    { return "x" }
func (l *listed) Voices() []Voice { return l.voices }
func (l *listed) Synthesize(context.Context, Voice, string, Params) (Audio, error) {
	return Audio{}, nil
}

func TestPickPrefersIdThenLanguageThenFirst(t *testing.T) {
	s := &Set{Engines: []Engine{&Espeak{}}}
	if _, _, err := s.Pick("", "it"); err == nil {
		t.Fatal("picked a voice from nothing")
	}
	e := &listed{voices: []Voice{{ID: "x:en-us", Language: "en-US", Engine: "x"}, {ID: "x:it", Language: "it", Engine: "x"}, {ID: "x:pt-br", Language: "pt-BR", Engine: "x"}}}
	s = &Set{Engines: []Engine{e}}
	for _, c := range []struct{ id, lang, want string }{
		{"x:pt-br", "it_IT.UTF-8", "x:pt-br"},
		{"", "it_IT.UTF-8", "x:it"},
		{"", "pt-BR", "x:pt-br"},
		{"", "pt_PT", "x:pt-br"},
		{"missing", "de", "x:en-us"},
	} {
		v, eng, err := s.Pick(c.id, c.lang)
		if err != nil || v.ID != c.want || eng != e {
			t.Errorf("%s %s: %v %s", c.id, c.lang, err, v.ID)
		}
	}
}
