# Cask

**Content-addressable storage library for Go.**

Module: `go.lostcrafters.com/cask`.

## Usage

```go
store, err := cask.New(existingDirectory, cask.Options{MaxBlobSize: 1 << 30})
if err != nil {
    return err
}
defer store.Close()

expected, err := cask.ParseDigest(expectedDigestText)
if err != nil {
    return err
}
info, err := store.Put(ctx, expected, source)
```

Import `go.lostcrafters.com/cask`. Keys use explicit SHA-256 or SHA-512 digests.
`Put` consumes and checks the supplied stream even for duplicates. New objects
are hashed once while writing, then synced and closed before atomic no-clobber
publication. Existing objects are independently verified before duplicate success;
corruption is reported, never repaired or overwritten.

`Open` returns an **unverified** reader and observed size; callers close it.
`Verify` explicitly hashes stored bytes. Verification is not permanent, and
`Verify` followed by `Open` is not an atomic verified read. Digest identity does
not establish publisher authenticity.

The root must already exist and be trusted. Local filesystems must support atomic
no-clobber publication: native exclusive rename on Linux/macOS/Windows, with a
hard-link fallback on Linux/macOS when exclusive rename is detectably unsupported.
No weaker copy fallback is used. Identifiable support failures return
`ErrUnsupportedFilesystem`; permissions, sharing violations, and I/O failures
retain their causes. File sync does not guarantee directory persistence across
power loss. A post-publication error can leave a complete object; retry is safe.

Methods can run concurrently across goroutines and cooperating processes.
`Close` must not race with store methods and does not close returned readers.
Contexts are checked between streaming operations; blocked readers are not
forcibly interrupted. Zero `MaxBlobSize` is unlimited; positive limits affect
ingestion only. Temporary files left by crashes are not automatically cleaned up.

See [the v1 design](docs/design.md) for the complete contract and threat model.

## Development

Use [mise](https://mise.jdx.dev/) for the pinned Go, golangci-lint, and hk tools:

```sh
mise trust
mise install
mise run fmt
mise run check
```

Additional tasks: `build`, `test`, `test:race` (requires a C compiler), `coverage`,
`lint`, `tidy`, and `vuln` (vulnerability scanning; needs network access for the
Go vulnerability database).

Install the repository-local hooks with
`mise run hooks:install`. Pre-commit formats and lints; pre-push runs tests and
checks module dependencies.
