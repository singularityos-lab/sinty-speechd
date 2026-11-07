package audio

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGainScalesAndClips(t *testing.T) {
	pcm := make([]byte, 6)
	for i, v := range []int16{1000, -20000, 30000} {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	out := Gain(pcm, 2)
	var got []int16
	for i := 0; i < len(out); i += 2 {
		got = append(got, int16(binary.LittleEndian.Uint16(out[i:])))
	}
	if !reflect.DeepEqual(got, []int16{2000, -32768, 32767}) {
		t.Fatalf("%v", got)
	}
	if &Gain(pcm, 1)[0] != &pcm[0] {
		t.Fatal("unity gain copied the buffer")
	}
}

func TestPlayerArguments(t *testing.T) {
	cases := map[string][]string{
		"/usr/bin/pw-play": {"--rate", "22050", "--channels", "1", "--format", "s16", "--media-role", "Accessibility", "-"},
		"paplay":           {"--raw", "--rate=22050", "--channels=1", "--format=s16le", "--property=media.role=a11y"},
		"aplay":            {"-q", "-t", "raw", "-f", "S16_LE", "-c", "1", "-r", "22050", "-"},
	}
	for bin, want := range cases {
		if got := (&Command{Argv: []string{bin}}).args(22050); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", bin, got)
		}
	}
	if got := (&Command{Argv: []string{"sox", "-r", "{rate}"}}).args(16000); !reflect.DeepEqual(got, []string{"-r", "16000"}) {
		t.Errorf("custom: %v", got)
	}
}

func TestCustomPlayerReceivesTheAudio(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.raw")
	c, err := Detect("sh -c cat>" + out)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Play(context.Background(), []byte("abcd"), 16000); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(out); string(data) != "abcd" {
		t.Fatalf("%q", data)
	}
}
