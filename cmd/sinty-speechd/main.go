package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-speechd/internal/audio"
	"github.com/singularityos-lab/sinty-speechd/internal/catalog"
	"github.com/singularityos-lab/sinty-speechd/internal/engine"
	"github.com/singularityos-lab/sinty-speechd/internal/service"
	"github.com/singularityos-lab/sinty-speechd/internal/speech"
	"github.com/singularityos-lab/sinty-speechd/internal/ssip"
)

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".local/share")
}

func cacheHome() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".cache")
}

func configHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

func socketPath() string {
	if a := os.Getenv("SPEECHD_ADDRESS"); strings.HasPrefix(a, "unix_socket:") {
		return strings.TrimPrefix(a, "unix_socket:")
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = filepath.Join(os.TempDir(), "sinty-speech-"+os.Getenv("USER"))
	}
	return filepath.Join(run, "speech-dispatcher", "speechd.sock")
}

func alive(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func spawn(path string) {
	if alive(path) {
		return
	}
	self, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if target, err := filepath.EvalSymlinks(self); err == nil {
		self = target
	}
	cmd := exec.Command(self)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	cmd.Process.Release()
	for i := 0; i < 50 && !alive(path); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !alive(path) {
		os.Exit(1)
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("sinty-speechd: ")
	if filepath.Base(os.Args[0]) == "speech-dispatcher" {
		spawn(socketPath())
		return
	}
	player := flag.String("player", os.Getenv("SINTY_SPEECH_PLAYER"), "command that plays raw 16-bit mono audio instead of pw-play, paplay or aplay")
	socket := flag.String("socket", socketPath(), "speech-dispatcher compatible socket")
	settings := flag.String("settings", filepath.Join(configHome(), "sinty-speech", "settings.json"), "file holding the chosen voice, rate, pitch and volume")
	voices := flag.String("voices", "/usr/share/sinty-speech/voices:"+filepath.Join(dataHome(), "sinty-speech", "voices"), "directories holding Piper voices")
	voiceList := flag.String("catalog", catalog.DefaultBase, "where the list of downloadable voices and the voices are published")
	flag.Parse()

	if alive(*socket) {
		log.Print("already running")
		return
	}
	if _, err := audio.Detect(*player); err != nil {
		log.Print(err)
	}
	out := &audio.Auto{Override: *player}
	piper := &engine.Piper{Binary: engine.Lookup("piper", "/usr/libexec/sinty-speech"), Dirs: filepath.SplitList(*voices)}
	engines := &engine.Set{Engines: []engine.Engine{
		piper,
		&engine.Espeak{Binary: engine.Lookup("espeak-ng")},
	}}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatal(err)
	}
	var svc *service.Service
	var server *ssip.Server
	queue := speech.NewQueue(engines, out, func(e speech.Event) {
		if svc != nil {
			svc.Event(e)
		}
		if server != nil {
			server.Event(e)
		}
	})
	dirs := filepath.SplitList(*voices)
	downloads := &catalog.Catalog{Base: *voiceList, Cache: filepath.Join(cacheHome(), "sinty-speech", "voices.json"), Dir: dirs[len(dirs)-1], MaxAge: 24 * time.Hour}
	svc, err = service.New(conn, queue, engines, *settings, downloads, piper.Binary != "")
	if err != nil {
		log.Fatal(err)
	}
	server = &ssip.Server{Queue: queue, Engines: engines, Defaults: func() (string, float64, float64, float64) {
		st := svc.Current()
		return st.Voice, st.Rate, st.Pitch, st.Volume
	}}
	reply, err := conn.RequestName(service.Name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatalf("%s is taken", service.Name)
	}

	os.MkdirAll(filepath.Dir(*socket), 0o700)
	os.Remove(*socket)
	l, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	os.Chmod(*socket, 0o600)
	go server.Serve(l)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go queue.Run(ctx)
	log.Printf("serving %s and %s with %d voices", service.Name, *socket, len(engines.Voices()))
	<-ctx.Done()
	l.Close()
	os.Remove(*socket)
}
