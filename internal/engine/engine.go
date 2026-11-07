package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Voice struct {
	ID       string
	Name     string
	Language string
	Engine   string
	model    string
}

type Audio struct {
	PCM        []byte
	SampleRate int
}

type Params struct {
	Rate  float64
	Pitch float64
}

type Engine interface {
	Name() string
	Voices() []Voice
	Synthesize(ctx context.Context, v Voice, text string, p Params) (Audio, error)
}

type Piper struct {
	Binary string
	Dirs   []string
}

func (p *Piper) Name() string { return "piper" }

type piperConfig struct {
	Dataset  string `json:"dataset"`
	Language struct {
		Code string `json:"code"`
	} `json:"language"`
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

func readPiperConfig(model string) (piperConfig, error) {
	var c piperConfig
	data, err := os.ReadFile(model + ".json")
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.Audio.SampleRate <= 0 {
		return c, errors.New("no sample rate in " + model + ".json")
	}
	return c, nil
}

func (p *Piper) Voices() []Voice {
	if p.Binary == "" {
		return nil
	}
	var out []Voice
	seen := map[string]bool{}
	for _, dir := range p.Dirs {
		models, _ := filepath.Glob(filepath.Join(dir, "*.onnx"))
		sort.Strings(models)
		for _, m := range models {
			c, err := readPiperConfig(m)
			if err != nil {
				continue
			}
			id := "piper:" + strings.TrimSuffix(filepath.Base(m), ".onnx")
			if seen[id] {
				continue
			}
			seen[id] = true
			name := c.Dataset
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(m), ".onnx")
			}
			out = append(out, Voice{ID: id, Name: name, Language: languageTag(c.Language.Code), Engine: p.Name(), model: m})
		}
	}
	return out
}

func (p *Piper) Synthesize(ctx context.Context, v Voice, text string, params Params) (Audio, error) {
	c, err := readPiperConfig(v.model)
	if err != nil {
		return Audio{}, err
	}
	scale := 1.0
	if params.Rate > 0 {
		scale = 1 / params.Rate
	}
	cmd := exec.CommandContext(ctx, p.Binary, "--model", v.model, "--output_raw", "--length_scale", strconv.FormatFloat(scale, 'f', 3, 64))
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(text, "\n", " ") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pcm, err := cmd.Output()
	if err != nil {
		return Audio{}, fmt.Errorf("piper: %w: %s", err, lastLine(stderr.String()))
	}
	return Audio{PCM: pcm, SampleRate: c.Audio.SampleRate}, nil
}

type Espeak struct {
	Binary string
}

func (e *Espeak) Name() string { return "espeak-ng" }

func (e *Espeak) Voices() []Voice {
	if e.Binary == "" {
		return nil
	}
	out, err := exec.Command(e.Binary, "--voices").Output()
	if err != nil {
		return nil
	}
	return parseEspeakVoices(out)
}

func parseEspeakVoices(list []byte) []Voice {
	var out []Voice
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(list))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] == "Pty" {
			continue
		}
		lang := f[1]
		if seen[lang] {
			continue
		}
		seen[lang] = true
		out = append(out, Voice{ID: "espeak-ng:" + lang, Name: strings.ReplaceAll(f[3], "_", " "), Language: languageTag(lang), Engine: "espeak-ng", model: lang})
	}
	return out
}

func (e *Espeak) Synthesize(ctx context.Context, v Voice, text string, p Params) (Audio, error) {
	wpm := 175.0
	if p.Rate > 0 {
		wpm *= p.Rate
	}
	pitch := 50.0
	if p.Pitch > 0 {
		pitch *= p.Pitch
	}
	pitch = math.Max(0, math.Min(99, pitch))
	cmd := exec.CommandContext(ctx, e.Binary, "--stdout", "-v", v.model, "-s", strconv.Itoa(int(wpm)), "-p", strconv.Itoa(int(pitch)), "--", text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	wav, err := cmd.Output()
	if err != nil {
		return Audio{}, fmt.Errorf("espeak-ng: %w: %s", err, lastLine(stderr.String()))
	}
	return ParseWAV(wav)
}

func ParseWAV(data []byte) (Audio, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Audio{}, errors.New("not a WAV stream")
	}
	rate := 0
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+16 > len(data) {
				return Audio{}, errors.New("short WAV format chunk")
			}
			if binary.LittleEndian.Uint16(data[body:]) != 1 || binary.LittleEndian.Uint16(data[body+2:]) != 1 || binary.LittleEndian.Uint16(data[body+14:]) != 16 {
				return Audio{}, errors.New("only 16-bit mono PCM is supported")
			}
			rate = int(binary.LittleEndian.Uint32(data[body+4:]))
		case "data":
			if rate == 0 {
				return Audio{}, errors.New("WAV data before its format")
			}
			end := body + size
			if size == 0 || size == 0xffffffff || end > len(data) {
				end = len(data)
			}
			return Audio{PCM: data[body:end], SampleRate: rate}, nil
		}
		pos = body + size + size%2
	}
	return Audio{}, errors.New("WAV without data")
}

func languageTag(code string) string {
	return strings.ReplaceAll(code, "_", "-")
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func Lookup(name string, extra ...string) string {
	for _, dir := range extra {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

type Set struct {
	Engines []Engine
}

func (s *Set) Voices() []Voice {
	var out []Voice
	for _, e := range s.Engines {
		out = append(out, e.Voices()...)
	}
	return out
}

func (s *Set) engine(name string) Engine {
	for _, e := range s.Engines {
		if e.Name() == name {
			return e
		}
	}
	return nil
}

func (s *Set) Pick(id, language string) (Voice, Engine, error) {
	voices := s.Voices()
	if id != "" {
		for _, v := range voices {
			if v.ID == id || v.Name == id {
				return v, s.engine(v.Engine), nil
			}
		}
	}
	if i := strings.IndexAny(language, ".@"); i >= 0 {
		language = language[:i]
	}
	if language != "" && language != "C" && language != "POSIX" {
		want := strings.ToLower(languageTag(language))
		base := strings.SplitN(want, "-", 2)[0]
		for _, exact := range []bool{true, false} {
			for _, v := range voices {
				got := strings.ToLower(v.Language)
				if exact && got == want || !exact && strings.SplitN(got, "-", 2)[0] == base {
					return v, s.engine(v.Engine), nil
				}
			}
		}
	}
	if len(voices) == 0 {
		return Voice{}, nil, errors.New("no voice is installed")
	}
	return voices[0], s.engine(voices[0].Engine), nil
}
