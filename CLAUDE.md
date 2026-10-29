# gh-maven-central

A `gh` CLI extension, written in Go, that sets up a GitHub repository (such as `../jpo-asn-pojos`)
to publish to Maven Central from GitHub Actions.  It generates an OpenPGP signing key, uploads the
public key to a keyserver, stores the key and a Central Portal token as environment secrets, sets
repository variables, and manages environments' required reviewers.  It does not write workflows.
See `README.md` for user-facing behavior.

The user is experienced with Java but new to Go.  When explaining Go code or idioms, relate them to
Java equivalents where that helps.

## Commands

```shell
go build                 # builds gh-maven-central(.exe)
go test ./...            # full suite; includes a gpg compatibility test if gpg is installed
go test -short ./...     # skips the gpg test (slow on Windows)
go vet ./...
gofmt -l .               # CI fails if this lists any files
gh extension install .   # install the local build as `gh maven-central`
```

CI (`.github/workflows/ci.yml`) runs gofmt, vet and tests on Linux and Windows.  Pushing a `v*`
tag runs `release.yml`, which builds the binaries `gh extension install` downloads.

## Layout

Single `package main`, no subpackages.

- `main.go`: command dispatch and exit codes (0 ok, 1 runtime error, 2 usage error via `usageError`).
- `app.go`: `app` (injectable dependencies), `session` (one command run against one repo), output
  helpers (`step`, `info`, `warn`), `write` for dry-run-aware changes, and flag validation.
- `setup.go`: the `setup` command.
- `reviewers.go`: the `reviewers` command.
- `github.go`: GitHub REST calls through the go-gh client (`requester`), secret sealing with NaCl box.
- `signing.go`: key generation and revocation certificate (ProtonMail/go-crypto).
- `keyserver.go`: HKP upload and polling.
- `fake_test.go`: in-memory fake GitHub API and keyserver, plus `newTestApp`.

## Conventions

- Dependencies are fields on `app` (writers, API client factory, HTTP client, env, terminal,
  clock) so tests can swap them.  Add new external dependencies the same way.
- Every change to GitHub or the keyserver goes through `session.write` or checks `s.dryRun`, so
  `--dry-run` never changes anything.
- Flags use the standard `flag` package via `newFlagSet` and `parseFlags`.  Usage text is a const
  per command and is kept in sync with `README.md`.
- Error messages are lowercase, user-facing, and wrap causes with `%w`.
- Tests use the fakes, not the network.  Use a real go-gh client against the fake server, and
  assert on `gh.writes`, captured stdout/stderr and exit codes.
- Keep the key's private material off disk; only the revocation certificate is written.

## Style

- No em dashes, in code, comments, docs or commit messages.
- Two spaces after periods in comments.
- Minimal comments: explain why, not what.  Don't comment obvious code.
- Not too verbose, in code, docs and responses.
- Run `gofmt` on changed files.
