# sinty-speechd

The speech service of Sinty OS. It turns text into speech for the screen
reader, for notifications read aloud and for any application that wants to
talk, with the voices installed on the computer.

## Voices

- **Piper** voices are used first. A voice is a `.onnx` model next to its
  `.onnx.json` configuration, in `/usr/share/sinty-speech/voices` or
  `~/.local/share/sinty-speech/voices`. The `piper` program is looked up in
  `/usr/libexec/sinty-speech` and then in `PATH`.
- **eSpeak NG** provides one voice for each language it knows, so there is
  always a voice when no Piper voice matches.

Without an explicit voice, the first voice of the language in `LANG` is
used, then any voice of the same language, then the first voice installed.

Text is spoken one sentence at a time: the next sentence is synthesized
while the current one plays, so long text starts speaking at once and stops
at once.

## D-Bus

The service owns `dev.sinty.Speech1` on the session bus at
`/dev/sinty/Speech1` and is started on demand.

| Member | Signature | |
|---|---|---|
| `Speak(text, voice, rate, pitch)` | in `ssdd`, out `u` | Queues text and returns its job. An empty voice and a rate or pitch of 0 use the settings. |
| `Announce(text)` | in `s`, out `u` | Interrupts what is being said and drops the queue. |
| `Preview(voice)` | in `s`, out `u` | Says a short sentence in the language of the voice. |
| `Stop()` | | Cancels everything the caller queued. |
| `ListVoices()` | out `aas` | Id, name, language and engine of every voice. |
| `Started(job)` | signal `u` | |
| `Sentence(job, offset, length)` | signal `uii` | The sentence being spoken, in characters of the text. |
| `Finished(job, completed)` | signal `ub` | `completed` is false when the job was cancelled or failed. |

`Voice`, `Rate` (0.25 to 4), `Pitch` (0.25 to 2) and `Volume` (0 to 2) are
writable properties kept in `~/.config/sinty-speech/settings.json`.
`Speaking` tells whether anything is being said.

```sh
gdbus call --session --dest dev.sinty.Speech1 --object-path /dev/sinty/Speech1 \
  --method dev.sinty.Speech1.Speak "Hello" "" 0 0
```

## speech-dispatcher clients

Orca, `spd-say` and the other speech-dispatcher clients connect to
`$XDG_RUNTIME_DIR/speech-dispatcher/speechd.sock` (or `SPEECHD_ADDRESS`),
where the service speaks the SSIP protocol: `SPEAK`, `CHAR`, `KEY`, `STOP`,
`CANCEL`, `SET` for the client name, priority, output module, language,
voice, rate, pitch, volume and notifications, `GET`, `LIST`, `BLOCK`,
`HISTORY GET CLIENT_ID` and `QUIT`, with the `BEGIN`, `END`, `CANCELLED` and
`INDEX MARK` notifications. SSML is accepted and its `mark` elements are
reported as index marks.

The `important` and `message` priorities interrupt lower priority speech and
drop it from the queue, as in speech-dispatcher.

`/usr/libexec/sinty-speech/speech-dispatcher` links to the service. Started
under that name, as clients do with `speech-dispatcher --spawn`, it starts
the service when its socket is not answering and returns.

## Audio

Speech is played with `pw-play`, `paplay` or `aplay`, the first one found,
with the accessibility media role. `-player` replaces it with any command
reading raw 16-bit mono audio on its standard input; `{rate}` in the command
is replaced by the sample rate.

## Building

```sh
make          # build
make test     # unit tests
make install  # static binary and D-Bus activation file under $(DESTDIR)$(PREFIX)
```
