package catalog

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func digest(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

type site struct {
	files    map[string]string
	requests atomic.Int32
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	body, ok := s.files[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write([]byte(body))
}

func serve(t *testing.T, model, config, modelSum string) (*Catalog, *site) {
	list := fmt.Sprintf(`{
 "it_IT-paola-medium": {"key": "it_IT-paola-medium", "name": "paola", "quality": "medium",
  "language": {"code": "it_IT"},
  "files": {
   "it/it_IT/paola/medium/it_IT-paola-medium.onnx": {"size_bytes": %d, "md5_digest": %q},
   "it/it_IT/paola/medium/it_IT-paola-medium.onnx.json": {"size_bytes": %d, "md5_digest": %q},
   "it/it_IT/paola/medium/MODEL_CARD": {"size_bytes": 3, "md5_digest": "x"}}},
 "en_US-amy-low": {"key": "en_US-amy-low", "name": "amy", "quality": "low", "language": {"code": "en_US"},
  "files": {"en/en_US/amy/low/en_US-amy-low.onnx": {"size_bytes": 1, "md5_digest": "x"},
   "en/en_US/amy/low/en_US-amy-low.onnx.json": {"size_bytes": 1, "md5_digest": "x"}}},
 "../escape": {"key": "../escape", "files": {"a.onnx": {}, "a.onnx.json": {}}}
}`, len(model), modelSum, len(config), digest(config))
	s := &site{files: map[string]string{
		"voices.json": list,
		"it/it_IT/paola/medium/it_IT-paola-medium.onnx":      model,
		"it/it_IT/paola/medium/it_IT-paola-medium.onnx.json": config,
	}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	return &Catalog{Base: srv.URL + "/", Cache: filepath.Join(dir, "cache", "voices.json"), Dir: filepath.Join(dir, "voices"), MaxAge: time.Hour}, s
}

func TestVoicesAreFilteredByLanguageAndCached(t *testing.T) {
	c, s := serve(t, "model", "{}", digest("model"))
	ctx := context.Background()
	it, err := c.Voices(ctx, "it_IT.UTF-8")
	if err != nil || len(it) != 1 || it[0].Key != "it_IT-paola-medium" || it[0].Language != "it-IT" || it[0].Size != 7 {
		t.Fatalf("%v %+v", err, it)
	}
	all, _ := c.Voices(ctx, "")
	if len(all) != 2 {
		t.Fatalf("an unsafe key was listed: %+v", all)
	}
	if s.requests.Load() != 1 {
		t.Fatalf("the list was fetched %d times", s.requests.Load())
	}
}

func TestInstallVerifiesAndPlacesTheVoice(t *testing.T) {
	c, _ := serve(t, "model-bytes", `{"audio":{"sample_rate":22050}}`, digest("model-bytes"))
	var last int64
	if err := c.Install(context.Background(), "it_IT-paola-medium", func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if !c.Installed("it_IT-paola-medium") || last != int64(len("model-bytes")+len(`{"audio":{"sample_rate":22050}}`)) {
		t.Fatalf("installed %v progress %d", c.Installed("it_IT-paola-medium"), last)
	}
	data, _ := os.ReadFile(filepath.Join(c.Dir, "it_IT-paola-medium.onnx"))
	if string(data) != "model-bytes" {
		t.Fatalf("%q", data)
	}
	if err := c.Remove("it_IT-paola-medium"); err != nil || c.Installed("it_IT-paola-medium") {
		t.Fatalf("remove: %v", err)
	}
	if err := c.Remove("../escape"); err == nil {
		t.Fatal("a path outside the voices was accepted")
	}
}

func TestTamperedDownloadIsRejected(t *testing.T) {
	c, _ := serve(t, "evil-bytes!", "{}", digest("model-bytes"))
	if err := c.Install(context.Background(), "it_IT-paola-medium", nil); err == nil {
		t.Fatal("a file with the wrong checksum was installed")
	}
	if c.Installed("it_IT-paola-medium") {
		t.Fatal("the model is in place")
	}
	left, _ := os.ReadDir(c.Dir)
	if len(left) != 0 {
		t.Fatalf("leftovers: %v", left)
	}
	if err := c.Install(context.Background(), "missing", nil); err == nil {
		t.Fatal("an unknown voice was installed")
	}
}
