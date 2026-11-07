package catalog

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultBase = "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/"

type File struct {
	Size int64  `json:"size_bytes"`
	MD5  string `json:"md5_digest"`
}

type entry struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Quality  string `json:"quality"`
	Language struct {
		Code    string `json:"code"`
		Native  string `json:"name_native"`
		English string `json:"name_english"`
		Country string `json:"country_english"`
	} `json:"language"`
	Files map[string]File `json:"files"`
}

type Voice struct {
	Key      string
	Name     string
	Language string
	Quality  string
	Size     int64
	model    string
	config   string
	files    map[string]File
}

type Catalog struct {
	Base   string
	Cache  string
	Dir    string
	Client *http.Client
	MaxAge time.Duration
}

func (c *Catalog) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *Catalog) get(ctx context.Context, name string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Base, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", name, resp.Status)
	}
	return resp, nil
}

func (c *Catalog) index(ctx context.Context) ([]byte, error) {
	if st, err := os.Stat(c.Cache); err == nil && time.Since(st.ModTime()) < c.MaxAge {
		if data, err := os.ReadFile(c.Cache); err == nil {
			return data, nil
		}
	}
	resp, err := c.get(ctx, "voices.json")
	if err != nil {
		if data, rerr := os.ReadFile(c.Cache); rerr == nil {
			return data, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("voice list: %w", err)
	}
	os.MkdirAll(filepath.Dir(c.Cache), 0o755)
	os.WriteFile(c.Cache, data, 0o644)
	return data, nil
}

func parse(data []byte) ([]Voice, error) {
	var raw map[string]entry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var out []Voice
	for key, e := range raw {
		v := Voice{Key: key, Name: e.Name, Language: strings.ReplaceAll(e.Language.Code, "_", "-"), Quality: e.Quality, files: e.Files}
		for name, f := range e.Files {
			switch {
			case strings.HasSuffix(name, ".onnx"):
				v.model = name
				v.Size += f.Size
			case strings.HasSuffix(name, ".onnx.json"):
				v.config = name
				v.Size += f.Size
			}
		}
		if v.model == "" || v.config == "" || !valid(key) {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func valid(key string) bool {
	return key != "" && !strings.ContainsAny(key, "/\\") && !strings.HasPrefix(key, ".")
}

func (c *Catalog) Voices(ctx context.Context, language string) ([]Voice, error) {
	data, err := c.index(ctx)
	if err != nil {
		return nil, err
	}
	all, err := parse(data)
	if err != nil {
		return nil, err
	}
	if language == "" {
		return all, nil
	}
	if i := strings.IndexAny(language, ".@"); i >= 0 {
		language = language[:i]
	}
	base := strings.ToLower(strings.SplitN(strings.ReplaceAll(language, "_", "-"), "-", 2)[0])
	var out []Voice
	for _, v := range all {
		if strings.ToLower(strings.SplitN(v.Language, "-", 2)[0]) == base {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *Catalog) find(ctx context.Context, key string) (Voice, error) {
	all, err := c.Voices(ctx, "")
	if err != nil {
		return Voice{}, err
	}
	for _, v := range all {
		if v.Key == key {
			return v, nil
		}
	}
	return Voice{}, fmt.Errorf("unknown voice %q", key)
}

func (c *Catalog) Installed(key string) bool {
	if !valid(key) {
		return false
	}
	_, err := os.Stat(filepath.Join(c.Dir, key+".onnx"))
	return err == nil
}

func (c *Catalog) Install(ctx context.Context, key string, progress func(done, total int64)) error {
	v, err := c.find(ctx, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	var done int64
	targets := map[string]string{v.model: key + ".onnx", v.config: key + ".onnx.json"}
	var temps []string
	defer func() {
		for _, t := range temps {
			os.Remove(t)
		}
	}()
	for _, name := range []string{v.config, v.model} {
		tmp, err := c.fetch(ctx, name, v.files[name], func(n int64) {
			if progress != nil {
				progress(done+n, v.Size)
			}
		})
		if err != nil {
			return err
		}
		temps = append(temps, tmp)
		done += v.files[name].Size
	}
	for i, name := range []string{v.config, v.model} {
		if err := os.Rename(temps[i], filepath.Join(c.Dir, targets[name])); err != nil {
			return err
		}
	}
	temps = nil
	return nil
}

func (c *Catalog) fetch(ctx context.Context, name string, want File, progress func(int64)) (string, error) {
	resp, err := c.get(ctx, name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(c.Dir, ".download-"+path.Base(name)+"-*")
	if err != nil {
		return "", err
	}
	sum := md5.New()
	var n int64
	buf := make([]byte, 256<<10)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			n += int64(k)
			if want.Size > 0 && n > want.Size {
				f.Close()
				os.Remove(f.Name())
				return "", fmt.Errorf("%s is larger than announced", name)
			}
			f.Write(buf[:k])
			sum.Write(buf[:k])
			progress(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(f.Name())
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	os.Chmod(f.Name(), 0o644)
	if want.Size > 0 && n != want.Size || want.MD5 != "" && !strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), want.MD5) {
		os.Remove(f.Name())
		return "", fmt.Errorf("%s does not match the voice list", name)
	}
	if want.MD5 == "" {
		os.Remove(f.Name())
		return "", errors.New(name + " has no checksum in the voice list")
	}
	return f.Name(), nil
}

func (c *Catalog) Remove(key string) error {
	if !valid(key) {
		return fmt.Errorf("invalid voice %q", key)
	}
	err := os.Remove(filepath.Join(c.Dir, key+".onnx"))
	os.Remove(filepath.Join(c.Dir, key+".onnx.json"))
	return err
}
