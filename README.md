# gh-maven-central

A [GitHub CLI](https://cli.github.com/) extension that sets up a repository to publish to
[Maven Central](https://central.sonatype.com/) from GitHub Actions.

## Why

Publishing to Maven Central from GitHub Actions needs a signing key and a Central Portal token in
every repository, usually in two deployment environments, such as one for snapshots and one for
releases. Doing that by hand for several repositories is tedious and error-prone:

- Each repository needs its own OpenPGP key, generated with `gpg`, exported, uploaded to a
  keyserver, and kept track of along with its passphrase and revocation certificate.
- The key, passphrase and token have to be copied and pasted into each environment's secrets, so
  it's easy to paste the wrong value, miss an environment, or leave a stray copy in a file, the
  clipboard or a shell history.
- Environments, reviewers and variables are set separately in the GitHub UI, and it's hard to see
  afterwards whether every repository was set up the same way.

This tool does it in one command per repository. The private key goes from memory straight into
the encrypted secrets, without being written to disk or pasted anywhere, and the names are the
same in every repository. `status` shows how a repository is set up, and `--dry-run` shows what a
change would do first.

## What it does

For each repository, it:

- Creates the deployment environments that don't exist, with a wait timer.
- With `--secrets`, generates an OpenPGP signing key and uploads its public key to a keyserver,
  where Maven Central looks it up to verify signatures. Then stores the signing key, its
  passphrase and a Central Portal user token as secrets in the environments.
- Sets or removes the repository variables that turn publishing on and set the groupId.
- Shows and changes the environments' required reviewers and wait timers.
- Shows how a repository is set up: environments, secret names and variables.

It doesn't write workflows: each repository keeps its own publishing workflows, which read the
secrets and variables by the [names](#names) below. It also doesn't do the steps that only the
Central Portal can do (see [Before you start](#before-you-start)).

## Names

The names are fixed, so every repository's workflows must use them:

| Name                       | Kind     | Where                    | Purpose                                                                  |
|----------------------------|----------|--------------------------|--------------------------------------------------------------------------|
| `MAVEN_CENTRAL_PUBLISH`    | Variable | Repository               | Set to `true` to enable publishing. The workflows are skipped in forks that don't set it. |
| `MAVEN_GROUP_ID`           | Variable | Repository               | Optional. Overrides the groupId, which otherwise defaults to `io.github.<owner>` (lowercase). |
| `CENTRAL_TOKEN_USERNAME`   | Secret   | Each `--env` environment | Central Portal user token username.                                      |
| `CENTRAL_TOKEN_PASSWORD`   | Secret   | Each `--env` environment | Central Portal user token password.                                      |
| `GPG_SIGNING_KEY`          | Secret   | Each `--env` environment | ASCII-armored private key used to sign the artifacts.                    |
| `GPG_SIGNING_KEY_PASSWORD` | Secret   | Each `--env` environment | Passphrase for the signing key.                                          |
| `TEST_ID`                  | Secret   | Each `--env` environment | Optional, from `setup --test-secret`. Just `test`; the workflows don't use it. |

## Install

The extension needs only `gh`, logged in (`gh auth login`) as a user with admin access to the
repositories you set up. It doesn't need `gpg`.

On Windows, install `gh`, and Go if you'll build from a local copy, with `winget`:

```shell
winget install --id GitHub.cli
winget install --id GoLang.Go
```

Open a new terminal afterwards so the updated `PATH` takes effect, then run `gh auth login`.
`winget upgrade --id GitHub.cli` and `winget upgrade --id GoLang.Go` update them later.

The extension is a Go program. `gh` downloads the binary for your platform from the latest
release:

```shell
gh extension install neaeraconsulting/gh-maven-central
gh extension upgrade maven-central    # update later
```

### From a local copy

To use the extension without installing a release from GitHub, such as to try out changes, build
it from a local copy of this repository. This also needs [Go](https://go.dev/dl/), at least the
version in `go.mod`.

```shell
cd gh-maven-central   # the folder must keep this name, which gh uses for the command name
go build              # creates gh-maven-central, or gh-maven-central.exe on Windows
gh extension install .
gh maven-central --help
```

On Windows, `gh extension install .` warns that there's no executable named `gh-maven-central`,
because it doesn't check for `gh-maven-central.exe`. The extension works anyway.

`gh extension install .` links to the folder rather than copying it, so after changing the code,
run `go build` again and `gh maven-central` runs the new build. `gh extension upgrade` doesn't
apply to a local install.

To switch between a local install and the GitHub release, remove the installed one first:

```shell
gh extension remove maven-central
```

You can also run the binary directly without installing it, such as
`./gh-maven-central status -R OWNER/REPO`. It uses the same `gh` login.

## Before you start

Do these steps in the [Central Portal](https://central.sonatype.com/), because they have no API:

1. Add and verify the namespace for the groupId under **Namespaces**, such as `io.github.<owner>`.
2. Generate a user token under **View Account > Generate User Token**. `setup --secrets` asks for
   it, or reads it from the `CENTRAL_TOKEN_USERNAME` and `CENTRAL_TOKEN_PASSWORD` environment
   variables.

## Example

This sets up a fork of jpo-asn-pojos, whose workflows publish releases from the
`maven-central-release` environment and snapshots from `maven-central-snapshot`, and only run when
the `MAVEN_CENTRAL_PUBLISH` variable is `true`:

```shell
# Preview the changes
gh maven-central setup -R neaeraconsulting/jpo-asn-pojos \
  --env maven-central-release --env maven-central-snapshot \
  --secrets --publish true \
  --dry-run

# Run it: the same command without --dry-run
gh maven-central setup -R neaeraconsulting/jpo-asn-pojos ...

# Require approval for releases from either of two users
gh maven-central reviewers -R neaeraconsulting/jpo-asn-pojos --env maven-central-release \
  --set LOGIN1 --set LOGIN2

# Later, turn publishing off without touching the key
gh maven-central variables -R neaeraconsulting/jpo-asn-pojos --publish false
```

Then publish a test snapshot to check the setup.

## setup

```
gh maven-central setup -R OWNER/REPO --env NAME [--env NAME]... [--secrets] [options]
```

| Option                         | Meaning                                                                         |
|--------------------------------|---------------------------------------------------------------------------------|
| `-R`, `--repo OWNER/REPO`      | Repository to set up (required)                                                 |
| `--env NAME`                   | Deployment environment (required, repeatable)                                   |
| `--secrets`                    | Also generate the signing key and store the [secrets](#names)                   |
| `--test-secret`                | Store the secret `TEST_ID`, whose value is just `test`, in every environment, to check that secrets can be stored there |
| `--remove-test-secret`         | Remove `TEST_ID` from every environment                                         |
| `--wait-timer MINUTES`         | Set the wait timer, which delays each job that uses the environments, in every environment; `0` for none. Without it, the environments it creates get 5 minutes |
| `--publish true\|false`        | Set `MAVEN_CENTRAL_PUBLISH`                                                     |
| `--remove-publish`             | Remove `MAVEN_CENTRAL_PUBLISH`, which turns publishing off                      |
| `--group-id GROUP`             | Set `MAVEN_GROUP_ID`                                                            |
| `--remove-group-id`            | Remove `MAVEN_GROUP_ID`, so the workflows use their default groupId             |
| `--key-name TEXT`              | With `--secrets`, the name in the key's user ID, `TEXT (OWNER/REPO)` (default: `<repo> release signing`) |
| `--out-dir DIR`                | With `--secrets`, where to save the revocation certificate (default: `~/.maven-central-keys`) |
| `--keyserver HOST`             | With `--secrets`, the keyserver for the public key (default: `keyserver.ubuntu.com`) |
| `--dry-run`                    | Run the read-only checks and show what would change, without changing it        |

`setup`:

1. Checks that you can administer the repository.
2. With `--secrets`, asks for the Central Portal token, and generates an RSA 4096 signing key with
   no expiry and no subkeys. Its user ID is `<repo> release signing (<owner>/<repo>)`, with no
   email address, so no address is published. The private key is encrypted with a random
   passphrase.
3. With `--secrets`, saves the key's [revocation certificate](#revoking-a-key) to `--out-dir`.
4. With `--secrets`, uploads the public key to the keyserver and waits until the keyserver returns
   it. If that fails, nothing on GitHub has been changed yet.
5. Creates the environments that don't exist, with a wait timer of `--wait-timer` minutes, or 5,
   which leaves time to cancel a job before it publishes. Existing environments' settings and
   reviewers are left as they are, except that `--wait-timer` sets their wait timer too. Wait timers on private repositories need a paid GitHub plan; without one,
   `setup` warns and creates the environments without a wait timer.
6. With `--secrets`, stores the key, passphrase and token in every environment, encrypted for
   GitHub on your machine. With `--test-secret` or `--remove-test-secret`, stores or removes
   `TEST_ID`.
7. Sets or removes the repository variables given with `--publish`, `--group-id` and their
   `--remove-` options. Variables without an option are left as they are.

The private key is never written to disk: once `setup` finishes, the GitHub secrets are the only
copy. That's enough, because the key is only for this repository and can be replaced at any time.

### Replacing the key

Run `setup --secrets` again to replace the key in every environment. To keep the token already
stored, leave the username blank when asked for it. Artifacts signed with the earlier key still
verify, because its public key stays on the keyserver.

### Token input on Windows

`setup --secrets` reads the token password without echoing it, which needs a console. Git Bash's
default mintty window isn't one: run the extension from Windows Terminal or PowerShell instead, or
set the `CENTRAL_TOKEN_USERNAME` and `CENTRAL_TOKEN_PASSWORD` environment variables.

## reviewers

```
gh maven-central reviewers -R OWNER/REPO --env NAME [--env NAME]...      # show environments
gh maven-central reviewers -R OWNER/REPO --env NAME [change options]     # change one environment
```

| Option                  | Meaning                                                              |
|-------------------------|----------------------------------------------------------------------|
| `--add LOGIN`           | Add a user as a reviewer (repeatable)                                |
| `--remove LOGIN`        | Remove a user, or a team by slug or `OWNER/slug` (repeatable)        |
| `--set LOGIN`           | Replace all reviewers with these users (repeatable)                  |
| `--clear`               | Remove all reviewers, so jobs run without approval                   |
| `--allow-self-review`   | Let the person who triggered a job approve it                        |
| `--prevent-self-review` | Don't let the person who triggered a job approve it                  |
| `--wait-timer MINUTES`  | Delay each job that uses the environment by this many minutes, up to 43200 (30 days); `0` removes the wait timer |
| `--dry-run`             | Show the result without changing anything                            |

An environment can have up to 6 reviewers, and any one of them can approve a job. Self-review is
prevented when an environment gets its first reviewers, unless `--allow-self-review` is given.
After that, the setting changes only when one of the self-review options is given. If you are the
only reviewer and self-review is prevented, you can't approve your own releases; the command warns
about that.

Changing reviewers keeps the environment's other settings, such as its wait timer and deployment
branch policy, and keeps team reviewers added in the GitHub UI unless `--set` or `--clear` replaces
the list. Required reviewers and wait timers need a public repository or a paid GitHub plan.

## variables

```
gh maven-central variables -R OWNER/REPO                    # show the variables
gh maven-central variables -R OWNER/REPO [change options]   # change them
```

| Option                    | Meaning                                                             |
|---------------------------|---------------------------------------------------------------------|
| `-R`, `--repo OWNER/REPO` | Repository (required)                                               |
| `--publish true\|false`   | Set `MAVEN_CENTRAL_PUBLISH`                                         |
| `--remove-publish`        | Remove `MAVEN_CENTRAL_PUBLISH`, which turns publishing off          |
| `--group-id GROUP`        | Set `MAVEN_GROUP_ID`                                                |
| `--remove-group-id`       | Remove `MAVEN_GROUP_ID`, so the workflows use their default groupId |
| `--dry-run`               | Show what would change, without changing anything                   |

Changes only the [variables](#names), so unlike `setup` it doesn't replace the key or ask for the
token. It doesn't check for admin access first: GitHub decides whether your login may read or
change the variables.

## status

```
gh maven-central status -R OWNER/REPO [--env NAME]...
```

| Option                         | Meaning                                                       |
|--------------------------------|---------------------------------------------------------------|
| `-R`, `--repo OWNER/REPO`      | Repository (required)                                         |
| `--env NAME`                   | Environment to show (repeatable; default: all of them)        |

Shows the repository's variables and secret names, the organization secrets it can use, and each
environment's reviewers, wait timer, deployment branches, secret names (with the date each was last
updated) and variables. It never shows secret values, and it changes nothing.

Unlike `setup` and `reviewers`, `status` doesn't need admin access. It shows whatever your `gh`
login can read, and marks anything else, such as secrets that need more access, as
`can't read` with GitHub's reason.

It marks the four [secrets](#names) as `missing` in the environments that have any of them or
`TEST_ID`, and in the environments given with `--env`. Other environments, such as one GitHub
creates for Copilot, aren't checked. `TEST_ID` is labeled as the test secret from
`setup --test-secret`.

It warns when an environment has a secret with the same name as a repository or organization
secret. Jobs that use the environment get the environment's value, but jobs that don't get the
other one, which is easy to miss when it holds an old key or token.

## Revoking a key

The revocation certificate can't sign anything, but it is the only way to revoke the key if the
private key leaks. Keep it somewhere the repository's maintainers can reach, such as a shared
password manager.

To revoke the key with `gpg`:

```shell
gpg --keyserver hkps://keyserver.ubuntu.com --recv-keys <fingerprint>
# First delete the ':' at the start of the "-----BEGIN PGP PUBLIC KEY BLOCK-----" line
gpg --import <owner>-<repo>-<keyid>.rev
gpg --keyserver hkps://keyserver.ubuntu.com --send-keys <fingerprint>
```

Then run `setup --secrets` again to give the repository a new key. After the revocation,
signatures made with the old key, including earlier ones, are reported as made by a revoked key.

## Development

```shell
go test ./...          # -short skips the gpg compatibility test, which is slow on Windows
go build
```

The tests run against an in-memory fake of the GitHub API and keyserver, so they don't need
network access or a GitHub login.

### CI

Every push to `main` and every pull request runs `.github/workflows/ci.yml`. It checks formatting
with `gofmt`, runs `go vet`, and runs the tests on Linux and Windows with the latest stable Go,
plus once on Linux with the minimum Go version in `go.mod`.

### Releases

`gh extension install` and `gh extension upgrade` download a prebuilt binary from the repository's
latest GitHub release, so users don't need Go. Releases are built by
`.github/workflows/release.yml`, which runs when a tag starting with `v` is pushed. It uses
[gh-extension-precompile](https://github.com/cli/gh-extension-precompile) to build the binaries
for each supported platform with the latest stable Go, and creates a release for the tag with
those binaries attached.

To make a release:

1. Check that CI passed on the commit to release, normally the latest commit on `main`.
2. Tag it with the next [semantic version](https://semver.org/) and push the tag:

   ```shell
   git checkout main
   git pull
   git tag v1.2.0
   git push origin v1.2.0
   ```

3. Watch the Release workflow under the repository's **Actions** tab, or with `gh run watch`.
   When it finishes, the release and its binaries are under **Releases**.
4. Check it from a machine with the extension installed: `gh extension upgrade maven-central`,
   then `gh maven-central --help`.

If the workflow fails, fix the problem, delete the tag locally and on GitHub
(`git tag -d v1.2.0` and `git push origin :refs/tags/v1.2.0`), delete the release if one was
created, and tag again. Don't move a tag that has a published release, because users may have
installed it; release a new version instead.

## License

[Apache License 2.0](LICENSE)
