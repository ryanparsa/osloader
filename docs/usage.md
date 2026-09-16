# Using osloader

Run `osloader` with no arguments for the picker, or use the subcommands for
scripts and CI.

```bash
osloader                                   # interactive picker
osloader list                              # table of available installers
osloader list --sort size --reverse        # date (default), version, size, name
osloader list --channel beta --json
osloader download --version 27.0           # pick by version (27 -> newest 27.x)
osloader download --build 26A428 --out ~/Downloads --connections 12
osloader url --version 27.0                # just print the URL

osloader list --os debian --filter arch=arm64 --filter media=cd
osloader list --os ubuntu --filter arch=amd64 --filter variant=desktop
osloader list --os fedora --filter variant=Workstation
osloader download --os ubuntu --product ubuntu-24.04.5.1-desktop-amd64.iso
```

## Flags

Everywhere: `--os`, `--channel`, `--filter` (repeatable), `--out` (defaults to
the current directory), `--connections` (max 16), `--limit-rate 20M`,
`--catalog-url`, `--user-agent`, `-v/--verbose`. On `download`: `--mirror`
(repeatable) and `--no-verify`.

`--user-agent` takes `default` (identifies osloader), `browser` (a current
desktop Chrome string), or any literal string.

There is no config file: defaults live in code, and everything is a flag or a
choice in the picker.

## Per-OS menus

Operating systems are not alike, so each one asks its own questions. Apple ships
one installer per release; Debian builds every image for several architectures
in CD and DVD sizes; Fedora has a dozen editions. A provider declares those
questions as *facets*, the picker turns each into a page, and the CLI exposes
them as `--filter key=value`. An unknown filter is rejected with the values that
OS actually offers.

Channels differ too - macOS has `public`, `devseed`, `beta` and `customerseed`;
Debian has `stable`, `live` and `testing`; Ubuntu has `lts` and `all`; Fedora
has `stable` and `prerelease` - and each OS defaults to its own first channel.

## Naming one exact release

`--build`, `--version` and `--product` can be combined, and all of them must
match, which makes a fully specified command unambiguous. The picker prints one
for every download, so the exact file can be scripted or sent to someone else:

```
osloader download --os macos --channel public --product 142-15488 --version 27.0 --build 26A428
```

If a selector matches several releases the newest is used, and the choice is
reported on stderr.

## In the picker

| Key | Does |
|---|---|
| `/` | search the list; the status bar shows the match count |
| `s` / `r` | cycle the sort (date, version, size, name) and reverse it |
| `b` / `f` | back and forward through the pages, like a browser |
| `q` | quit, or stop a running download and save its progress |

The header shows the active channel, filters and sort order, so a short list
always explains itself.
