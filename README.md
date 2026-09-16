# osloader

Find official OS installers, download them **fast**, and know they are **real**.

![The osloader picker, choosing an operating system](docs/screenshot.png)

**Fast** - one file pulled through up to 16 connections at once, spread across
the mirrors osloader just timed for you, resuming exactly where it stopped.

**Secure** - nothing gets its real file name until it matches what the vendor
published: an Apple-signed package chain, or the SHA-256 the project signed.
A file that fails is quarantined, not handed over.

| OS | Source | What proves the file |
|---|---|---|
| macOS | Apple's Software Update catalog | `pkgutil` signature, Apple Root CA chain |
| Debian | cdimage.debian.org, ~90 measured mirrors | SHA-256 from the signed `SHA256SUMS` |
| Ubuntu | releases.ubuntu.com, cdimage.ubuntu.com | SHA-256 from the signed `SHA256SUMS` |
| Fedora | fedoraproject.org `releases.json` | SHA-256 and size published by Fedora |
| Windows | - | registered, not implemented yet |

## Start

```bash
go build -o osloader ./cmd && ./osloader
```

```bash
osloader                                             # interactive picker
osloader list --os ubuntu --filter arch=amd64        # or drive it from a script
osloader download --os debian --product debian-13.7.0-amd64-netinst.iso
```

Prebuilt binaries for Linux, Windows and macOS are on the
[releases page](https://github.com/ryanparsa/osloader/releases) - see
[Installing](docs/install.md), which also covers the macOS Gatekeeper prompt.

## Docs

- [Installing](docs/install.md) - binaries, building, Gatekeeper
- [Using osloader](docs/usage.md) - commands, flags, per-OS menus, keys
- [How the download works](docs/downloading.md) - connections, mirrors, resume
- [How a download is verified](docs/verifying.md) - what each OS proves
- [Adding an operating system](docs/providers.md) - the provider interface

## License

MIT. Contributions welcome.
