# Development

```bash
go test -race ./...

# A fake box on 127.0.0.1:50001, about as slow as the real one, and the app pointed at it.
go run ./cmd/fakebox -rate 2000000 -read-delay 1s &
go run ./cmd/merlin-connect -box 127.0.0.1:50001 -data /tmp/merlin-dev

# Hardware checks against a real box (see protocol.md).
go run ./cmd/merlin-connect probe -h
```

## Releases

Pushing a `v*` tag builds Linux and Windows binaries and publishes a GitHub release
([release.yml](../.github/workflows/release.yml)):

```bash
git tag v1.0.1 && git push origin v1.0.1
```

See [design.md](design.md) for how the app works and [protocol.md](protocol.md) for the
box protocol.
