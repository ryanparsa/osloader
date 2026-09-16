# osloader

Find official OS installers, download them fast, and verify that what landed on
disk really is the vendor's file.

macOS (Apple's Software Update catalog) and Debian (cdimage.debian.org) are
implemented. Windows is registered behind the same interface and reports itself
as not implemented yet.

Operating systems are not alike, so each one asks its own questions: Apple ships
one installer per release, while Debian builds every image for several
architectures in CD and DVD sizes. A provider declares those questions as
*facets*, the picker turns each into a page, and the CLI exposes them as
`--filter key=value`. Questions that shape the list (architecture, image size)
come before it; questions that shape the download (which mirror) come after you
have chosen a release.

```bash
go build -o osloader ./cmd && ./osloader
go run ./cmd list            # or: go run cmd/main.go list
```

Builds are published for Linux (amd64, arm64), Windows (amd64, arm64) and
macOS (arm64), with a `SHA256SUMS` alongside them.

(`go install github.com/ryanparsa/osloader/cmd@latest` installs it as `cmd` —
move the package to `cmd/osloader/` if you want that name to be `osloader`.)

## Use it

```bash
osloader                                   # interactive picker
osloader list                              # table of available installers
osloader list --sort size --reverse        # date (default), version, size, name
osloader list --channel beta --json
osloader download --version 27.0           # pick by version (27 → newest 27.x)
osloader download --build 26A428 --out ~/Downloads --connections 12
osloader url --version 27.0                # just print the URL

osloader list --os debian                              # Debian images
osloader list --os debian --filter arch=arm64 --filter media=cd
osloader download --os debian --product debian-13.7.0-amd64-netinst.iso
```

Flags that apply everywhere: `--os`, `--channel` (`public`, `devseed`, `beta`,
`customerseed`), `--out` (defaults to the current directory), `--connections`
(max 16), `--limit-rate 20M`, `--catalog-url`, `--user-agent`, `-v/--verbose`, and `--mirror` (repeatable) on
`download`.

`--build`, `--version` and `--product` can be combined, and all of them must
match. That makes a fully specified command unambiguous: the TUI prints one for
every download, so the exact file can be scripted or sent to someone else —

```
osloader download --os macos --channel public --product 142-15488 --version 27.0 --build 26A428
```

If a selector matches several releases, the newest is used and the choice is
reported on stderr.

In the release list, `s` cycles the sort — date, version, size, name — and `r`
reverses it. The header shows the current order, and the cursor stays on the
release you had selected.

`--user-agent` takes `default` (identifies osloader), `browser` (a current
desktop Chrome string), or any literal string you pass.

There is no config file: defaults live in code, and everything is a flag or a
choice in the TUI.

## Downloading

The transfer is split into up to 16 byte ranges fetched in parallel and written
straight to their offsets in a pre-allocated file. Memory use is fixed — one
256 KiB buffer per connection — so a 17 GiB installer costs the same RAM as a
small one.

Downloads go to the vendor's own server unless you ask for something else — a
mirror is a choice, never a default. After you pick a Debian image it asks
where to fetch it from, and the menu is Debian's own published mirror list
(`Mirrors.masterlist`, ~90 sites with country and city) plus two measured
options:

| Choice | What it does |
|---|---|
| `official` (default) | cdimage.debian.org, one source |
| `fastest` | times every mirror on the real file — a 16 KiB latency screen across all of them, then a 256 KiB throughput read from the quickest six — and uses the winner |
| `fastest3` | same measurement, then spreads the connections across the top three |
| `<host>` | that mirror, e.g. `--filter mirror=ftp.fau.de` |

`--mirror <url>` still adds sources by hand on any OS. Weekly (testing) builds
are not mirrored, so only Debian's own server is offered there.

When there is more than one source, mirrors are probed before use and dropped if
they serve a different size or refuse ranges — chunks from two different files
would corrupt the result. Connections spread across the healthy mirrors, each
retry moves to another one, and three consecutive failures retire a mirror for
the rest of the run.

The download screen gives each connection its own line — the chunk it holds,
how far into it, and its own speed — so one stalled connection is obvious while
the others keep moving. `--verbose` adds an event log: preflight, chunk plan,
each chunk start and finish, retries with their cause, stalls, and every
verification check. On the CLI it streams to stderr (stdout stays pipeable);
in the TUI the last few lines appear under the connections. When a file has
several mirrors, each connection also shows which host it is reading from.

Every list page can be searched: `/` opens a search box and the status bar
shows the match count — which is how you get through ninety mirrors or a
catalogue of images. In the picker, `b` goes back and `f` goes forward, like a browser — the
page comes back with its cursor where you left it, and a catalog that arrives
after you have moved on is ignored. `q` during a download stops it, waits for
the resume state to be written, and then prints where it got to and the command
that finishes the job:

```
download stopped at 33% (5.6 GiB of 17.1 GiB, kept in ./macOS_27.0_26A428_InstallAssistant.pkg.part)
resume with: osloader download --os macos --channel public --product 142-15488 --version 27.0 --build 26A428
```

Progress is recorded in a sidecar next to the partial file:

```
macOS_27.0_26A428_InstallAssistant.pkg.part
macOS_27.0_26A428_InstallAssistant.pkg.part.json
```

Interrupt with Ctrl-C (or `q` in the TUI) and run the same command again: each
range continues from its saved offset. If the server's ETag, size or
modification time has moved, the saved bytes belong to a different file and are
discarded rather than mixed in. Stalled connections are dropped after 60s,
transient failures (5xx, 429, resets) are retried with backoff, and a server
that ignores `Range` falls back to a single stream.

## Verifying

A download only gets its real file name after its checks pass. What "verified"
means depends on what the vendor publishes, and the report always says which
check did the work.

**macOS** — the installer is a signed package from a single Apple CDN:

1. **Size** matches the catalog and the server's `Content-Length`.
2. **Provenance** — HTTPS, and every redirect hop stays on Apple's hosts.
3. **SHA-256** computed and shown. Informational only: Apple publishes no
   per-file hash to compare against (the catalog `Digest` is not a file hash).
4. **Signature** — `pkgutil --check-signature` must report an Apple-trusted
   chain ending in Apple Root CA.

**Debian** — images come from a mirror network, so the transport proves nothing
and the digest proves everything:

1. **SHA-256** must match the `SHA256SUMS` published for that directory, read
   over HTTPS from Debian's own origin (and signed alongside as
   `SHA256SUMS.sign`). A mismatch is fatal.
2. Size is reported for information; Debian publishes digests, not byte counts.

A file that fails is kept as `<name>.unverified` and the command exits non-zero.
On macOS off-platform the signature step cannot run and says so — it is never
reported as a pass. `--no-verify` skips the lot, and is not recommended.

## Adding an OS

Implement `provider.Provider` (`Name`, `Available`, `Channels`, `Facets`,
`List`, `Resolve`) in `internal/provider/<os>` and register it from `init`.
Declare whatever questions that OS needs as facets, and return an `Artifact`
with the download URL, any mirrors, the hosts it may come from, and a
`verify.Verifier` — `verify.ApplePkg` for a signed package, `verify.Checksum`
for a published digest. The TUI, the CLI and the download engine need no
changes.

## Layout

```
cmd/main.go          entry point
internal/cli/        cobra commands
internal/tui/        Bubble Tea picker and progress view
internal/provider/   Provider interface, facets, registry; macos/, debian/, windows/
internal/download/   chunk planner, workers, resume state, progress
internal/verify/     size, sha256, pkgutil signature (darwin build tag)
internal/format/     byte, rate and duration rendering
internal/logging/    compact verbose log + ring buffer for the TUI
internal/ua/         User-Agent strings
```
