# Installing

## Prebuilt binaries

Every tagged release publishes binaries for Linux (amd64, arm64), Windows
(amd64, arm64) and macOS (arm64), each with a `SHA256SUMS` alongside.

```bash
curl -L https://github.com/ryanparsa/osloader/releases/latest/download/osloader_v0.1.0_darwin_arm64.tar.gz | tar xz
./osloader
```

## From source

```bash
go build -o osloader ./cmd && ./osloader
go run ./cmd list                  # or: go run cmd/main.go list
```

`go install github.com/ryanparsa/osloader/cmd@latest` works too, but installs
the binary as `cmd`; move the package to `cmd/osloader/` if that name matters.

## macOS Gatekeeper

The macOS build is not signed with an Apple Developer ID, so a binary downloaded
through a browser carries `com.apple.quarantine` and Gatekeeper refuses to run
it: *"Apple could not verify 'osloader' is free of malware"*.

Downloading with `curl`, as above, never sets that flag. For a file already
downloaded through a browser:

```bash
xattr -d com.apple.quarantine ./osloader
```

Building it yourself avoids the question entirely.
