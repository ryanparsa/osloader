# Adding an operating system

Implement `provider.Provider` in `internal/provider/<os>` and register it from
`init`:

```go
type Provider interface {
    Name() string
    Available() bool
    Channels() []Channel
    Facets(channel string) []Facet
    List(ctx context.Context, channel string, sel Selection) ([]Release, error)
    Resolve(ctx context.Context, r Release, sel Selection) (Artifact, error)
}
```

Declare whatever questions that OS needs as facets - `StageList` for the ones
that shape the list (architecture, edition), `StageResolve` for the ones that
shape the download - and the picker turns each into a page while the CLI exposes
it as `--filter key=value`. Implement `Description() string` to say where the
images come from; the OS menu shows it.

`Resolve` returns an `Artifact`:

```go
type Artifact struct {
    URL           string   // canonical source
    Mirrors       []string // other sources for the same bytes
    Filename      string
    Size          int64
    AllowedHosts  []string // empty means the digest is the only gate
    MaxPerHost    int      // connections one host may get
    PreferPrimary bool     // use URL, keeping Mirrors as spares
    Digest        string
    Verifier      verify.Verifier
}
```

Use `verify.ApplePkg` for a signed package or `verify.Checksum` for a published
digest. Pin `AllowedHosts` when the vendor serves the file itself; leave it
empty when mirrors are involved and let the digest do the work.

The picker, the CLI and the download engine need no changes.

## Layout

```
cmd/main.go          entry point
internal/cli/        cobra commands
internal/tui/        Bubble Tea picker and progress view
internal/provider/   Provider interface, facets, registry
                     macos/, debian/, ubuntu/, fedora/, windows/, webdir/
internal/download/   chunk planner, workers, mirrors, resume state, progress
internal/verify/     size, sha256, pkgutil signature (darwin build tag)
internal/format/     byte, rate and duration rendering
internal/logging/    compact verbose log + ring buffer for the picker
internal/ua/         User-Agent strings
```

`internal/provider/webdir` holds the checksum and directory-index parsing that
Debian and Ubuntu share.

## Tests

Provider tests run offline against fixtures - no test reaches the network.
`internal/download` covers the engine against an `httptest` server with range
support: parallel download, resume after cancellation, mirror failover, a server
that ignores ranges, injected 503s, a heap ceiling while downloading 256 MiB,
and `goleak` for stray goroutines.
