# How the download works

## Many connections, one file

A transfer is split into up to 16 byte ranges fetched in parallel and written
straight to their offsets in a pre-allocated file. Memory use is fixed - one
256 KiB buffer per connection - so a 17 GiB installer costs the same RAM as a
small one, and nothing is buffered in memory waiting to be written.

## Sources are measured, not chosen

Where a download comes from is the tool's problem, not a menu to answer. Debian
publishes around ninety mirrors and no way to know which is near this machine,
so before a Debian download starts every mirror is timed against the file
itself - a 16 KiB latency screen across all of them, then a 256 KiB throughput
read from the quickest ten - and those ten become the sources. Ubuntu serves its
own images and Fedora redirects each request to a nearby mirror, so neither
needs measuring. Apple serves a single CDN.

With more than one source, each is probed before use and dropped if it serves a
different size or refuses range requests; chunks from two different files would
corrupt the result. Connections spread across the healthy sources, a definitive
failure (403, 404) retires that source and the chunk carries on elsewhere, and
three consecutive failures retire it for the rest of the run. Public mirrors are
not CDNs, so each host gets at most four connections.

`--mirror <url>` adds a source by hand on any OS.

## Stopping and resuming

Progress is recorded in a sidecar next to the partial file:

```
macOS_27.0_26A428_InstallAssistant.pkg.part
macOS_27.0_26A428_InstallAssistant.pkg.part.json
```

Interrupt with Ctrl-C (or `q` in the picker, which waits for the sidecar to be
written) and run the same command again: each range continues from its saved
offset. On exit the terminal gets the state and the command that finishes the
job:

```
download stopped at 33% (5.6 GiB of 17.1 GiB, kept in ./macOS_27.0_26A428_InstallAssistant.pkg.part)
resume with: osloader download --os macos --channel public --product 142-15488 --version 27.0 --build 26A428
```

If the server's ETag, size or modification time has moved, the saved bytes
belong to a different file and are discarded rather than mixed in.

## When things go wrong

Stalled connections are dropped after 60s of silence, transient failures (5xx,
429, resets) are retried with exponential backoff and jitter, and a server that
ignores `Range` falls back to a single stream.

## Watching it

The download screen gives each connection its own line - the chunk it holds, how
far into it, its own speed, and which host it is reading from when there are
several - so one stalled connection is obvious while the others keep moving.

`--verbose` adds an event log: preflight, chunk plan, each chunk start and
finish, retries with their cause, stalls, mirror measurements and every
verification check. On the CLI it streams to stderr, leaving stdout pipeable; in
the picker the last few lines appear under the connections.
