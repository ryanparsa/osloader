# How a download is verified

A download only gets its real file name after its checks pass. What "verified"
means depends on what the vendor publishes, and the report always says which
check did the work.

## macOS

The installer is a signed package served from a single Apple CDN:

1. **Size** matches the catalog and the server's `Content-Length`.
2. **Provenance** - HTTPS, and every redirect hop stays on Apple's hosts.
3. **SHA-256** computed and shown. Informational only: Apple publishes no
   per-file hash to compare against (the catalog `Digest` is not a file hash).
4. **Signature** - `pkgutil --check-signature` must report an Apple-trusted
   chain ending in Apple Root CA.

## Debian, Ubuntu and Fedora

Images come from mirror networks, so the transport proves nothing and the digest
proves everything:

1. **SHA-256** must match what the project published: Debian's and Ubuntu's
   `SHA256SUMS` (signed alongside as `SHA256SUMS.gpg`), or Fedora's
   `releases.json`, each read over HTTPS from the project's own origin. A
   mismatch is fatal.
2. **Size** is a second gate where the project publishes one (Fedora does).
   Debian and Ubuntu publish digests rather than byte counts, so their size is
   reported for information.

This is why a mirror can be any host at all: nothing about the file is taken on
trust from the server that served it.

## When a check fails

The file is kept as `<name>.unverified` and the command exits non-zero, so a
failed download is never sitting in a folder under a name that suggests it is
fine. A file that is already present is re-checked rather than assumed good, and
re-downloading is refused until it is removed.

Off macOS the signature step cannot run and says so - it is never reported as a
pass. `--no-verify` skips everything, and is not recommended.
