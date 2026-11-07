package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Player interface {
	Play(ctx context.Context, pcm []byte, rate int) error
}

type Command struct {
	Argv []string
}

func Detect(override string) (*Command, error) {
	if override != "" {
		argv := strings.Fields(override)
		if len(argv) == 0 {
			return nil, errors.New("empty player command")
		}
		return &Command{Argv: argv}, nil
	}
	for _, name := range []string{"pw-play", "paplay", "aplay"} {
		if p, err := exec.LookPath(name); err == nil {
			return &Command{Argv: []string{p}}, nil
		}
	}
	return nil, errors.New("no audio player found (pw-play, paplay or aplay)")
}

type Auto struct {
	Override string
}

func (a *Auto) Play(ctx context.Context, pcm []byte, rate int) error {
	c, err := Detect(a.Override)
	if err != nil {
		return err
	}
	return c.Play(ctx, pcm, rate)
}

func (c *Command) args(rate int) []string {
	r := strconv.Itoa(rate)
	base := c.Argv[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	extra := c.Argv[1:]
	switch base {
	case "pw-play", "pw-cat":
		return append(append([]string{}, extra...), "--rate", r, "--channels", "1", "--format", "s16", "--media-role", "Accessibility", "-")
	case "paplay":
		return append(append([]string{}, extra...), "--raw", "--rate="+r, "--channels=1", "--format=s16le", "--property=media.role=a11y")
	case "aplay":
		return append(append([]string{}, extra...), "-q", "-t", "raw", "-f", "S16_LE", "-c", "1", "-r", r, "-")
	}
	out := make([]string, 0, len(extra))
	for _, a := range extra {
		out = append(out, strings.ReplaceAll(a, "{rate}", r))
	}
	return out
}

func (c *Command) Play(ctx context.Context, pcm []byte, rate int) error {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.args(rate)...)
	cmd.Stdin = bytes.NewReader(pcm)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w: %s", c.Argv[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func Gain(pcm []byte, volume float64) []byte {
	if volume >= 0.999 && volume <= 1.001 {
		return pcm
	}
	if volume < 0 {
		volume = 0
	}
	out := make([]byte, len(pcm)&^1)
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) * volume
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[i:], uint16(int16(v)))
	}
	return out
}
