# Cask

**Content-addressable storage library for Go.**

Module: `go.lostcrafters.com/cask`.

## Status

Scaffold only; no public API or storage implementation yet. Library code belongs
at the module root, with subpackages introduced as needed.

## Development

Use [mise](https://mise.jdx.dev/) for the pinned Go, golangci-lint, and hk tools:

```sh
mise trust
mise install
mise run fmt
mise run check
```

Additional tasks: `build`, `test`, `test:race` (requires a C compiler), `coverage`,
`lint`, and `tidy`.

Once this directory is a Git repository, install the local hooks with
`mise run hooks:install`. Pre-commit formats and lints; pre-push runs tests and
checks module dependencies.
