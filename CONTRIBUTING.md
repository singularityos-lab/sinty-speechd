# Contributing to sinty-speechd

## Quick Start

```bash
git clone https://github.com/singularityos-lab/sinty-speechd
cd sinty-speechd
go build ./...
go test ./...
```

## Guidelines

- Keep changes small and tested. A screen reader user depends on this
  service to use the computer at all: speech must start and stop at once.
- What speech-dispatcher clients see must not change: check a change with
  `spd-say` and Orca before sending it.
- Match the surrounding style.
- By submitting a contribution you agree to the [CLA](CLA.md).

## License

GPL-3.0-only.
