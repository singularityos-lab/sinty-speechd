BIN := sinty-speechd
PREFIX ?= /usr

.PHONY: all build static test vet install clean

all: build

build:
	go build -o $(BIN) ./cmd/sinty-speechd

static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/sinty-speechd

test:
	go test ./...

vet:
	go vet ./...

install: static
	install -Dm755 $(BIN) $(DESTDIR)$(PREFIX)/libexec/$(BIN)
	install -d $(DESTDIR)$(PREFIX)/libexec/sinty-speech $(DESTDIR)$(PREFIX)/share/sinty-speech/voices
	ln -sf ../$(BIN) $(DESTDIR)$(PREFIX)/libexec/sinty-speech/speech-dispatcher
	install -Dm644 packaging/dev.sinty.Speech1.service $(DESTDIR)$(PREFIX)/share/dbus-1/services/dev.sinty.Speech1.service

clean:
	rm -f $(BIN)
