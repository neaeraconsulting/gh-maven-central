# gh-maven-central

A [GitHub CLI](https://cli.github.com/) extension that sets up a repository to publish to
[Maven Central](https://central.sonatype.com/) from GitHub Actions. For each repository, it:

- Generates an OpenPGP signing key and uploads its public key to a keyserver, where Maven Central
  looks it up to verify signatures.
- Stores the signing key, its passphrase and a Central Portal user token as secrets in one or more
  deployment environments, creating any environments that don't exist.
- Sets repository variables, such as one that turns publishing on.
- Shows and changes the environments' required reviewers.

It doesn't write workflows: each repository keeps its own publishing workflows, which read the
secrets and variables by the names you give the tool. It also doesn't do the steps that only the
Central Portal can do (see [Before you start](#before-you-start)).

## Install

The extension is a Go program. `gh` downloads the binary for your platform from the latest
release:

```shell
gh extension install iyourshaw/gh-maven-central
gh extension upgrade maven-central    # update later
```

To run it from a working copy instead, build it there and install the folder:

```shell
go build
gh extension install .
```

It needs only `gh`, logged in (`gh auth login`) as a user with admin access to the repositories you
set up. It doesn't need `gpg`.

## Before you start

Do these steps in the [Central Portal](https://central.sonatype.com/), because they have no API:

1. Add and verify the namespace for the groupId under **Namespaces**, such as `io.github.<owner>`.
2. Generate a user token under **View Account > Generate User Token**. `setup` asks for it, or
   reads it from the `CENTRAL_TOKEN_USERNAME` and `CENTRAL_TOKEN_PASSWORD` environment variables.

## Example

This sets up a fork of jpo-asn-pojos, whose workflows publish releases from the
`maven-central-release` environment and snapshots from `maven-central-snapshot`, and only run when
the `MAVEN_CENTRAL_PUBLISH` variable is `true`:

```shell
# Preview the changes
gh maven-central setup -R CDOT-CV/jpo-asn-pojos \
  --env maven-central-release --env maven-central-snapshot \
  --key-secret GPG_SIGNING_KEY --passphrase-secret GPG_SIGNING_KEY_PASSWORD \
  --token-username-secret CENTRAL_TOKEN_USERNAME --token-password-secret CENTRAL_TOKEN_PASSWORD \
  --variable MAVEN_CENTRAL_PUBLISH=true \
  --dry-run

# Run it: the same command without --dry-run
gh maven-central setup -R CDOT-CV/jpo-asn-pojos ...

# Require approval for releases from alice or bob
gh maven-central reviewers -R CDOT-CV/jpo-asn-pojos --env maven-central-release --set alice --set bob
```

Then publish a test snapshot to check the setup.

## setup

```
gh maven-central setup -R OWNER/REPO --env NAME [--env NAME]...
    --key-secret NAME --passphrase-secret NAME
    --token-username-secret NAME --token-password-secret NAME [options]
```

| Option                         | Meaning                                                                         |
|--------------------------------|---------------------------------------------------------------------------------|
| `-R`, `--repo OWNER/REPO`      | Repository to set up (required)                                                 |
| `--env NAME`                   | Deployment environment for the secrets (required, repeatable)                   |
| `--key-secret NAME`            | Secret for the ASCII-armored, passphrase-protected private key (required)       |
| `--passphrase-secret NAME`     | Secret for the key's passphrase (required)                                      |
| `--token-username-secret NAME` | Secret for the Central Portal token username (required)                         |
| `--token-password-secret NAME` | Secret for the Central Portal token password (required)                         |
| `--variable NAME=VALUE`        | Repository variable to set (repeatable)                                         |
| `--key-name TEXT`              | Name in the key's user ID, `TEXT (OWNER/REPO)` (default: `<repo> release signing`) |
| `--out-dir DIR`                | Where to save the revocation certificate (default: `~/.maven-central-keys`)     |
| `--keyserver HOST`             | Keyserver for the public key (default: `keyserver.ubuntu.com`)                  |
| `--dry-run`                    | Run the read-only checks and show what would change, without changing it        |

`setup`:

1. Checks that you can administer the repository, and asks for the Central Portal token.
2. Generates an RSA 4096 signing key with no expiry and no subkeys. Its user ID is
   `<repo> release signing (<owner>/<repo>)`, with no email address, so no address is published.
   The private key is encrypted with a random passphrase.
3. Saves the key's [revocation certificate](#revoking-a-key) to `--out-dir`.
4. Uploads the public key to the keyserver and waits until the keyserver returns it. If that
   fails, nothing on GitHub has been changed yet.
5. Creates the environments that don't exist. Existing environments' settings and reviewers are
   left as they are.
6. Stores the key, passphrase and token in every environment, encrypted for GitHub on your
   machine.
7. Sets the repository variables.

The private key is never written to disk: once `setup` finishes, the GitHub secrets are the only
copy. That's enough, because the key is only for this repository and can be replaced at any time.

### Replacing the key

Run `setup` again to replace the key in every environment. To keep the token already stored, leave
the username blank when asked for it. Artifacts signed with the earlier key still verify, because
its public key stays on the keyserver.

### Token input on Windows

`setup` reads the token password without echoing it, which needs a console. Git Bash's default
mintty window isn't one: run the extension from Windows Terminal or PowerShell instead, or set the
`CENTRAL_TOKEN_USERNAME` and `CENTRAL_TOKEN_PASSWORD` environment variables.

## reviewers

```
gh maven-central reviewers -R OWNER/REPO --env NAME [--env NAME]...      # show reviewers
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
| `--dry-run`             | Show the result without changing anything                            |

An environment can have up to 6 reviewers, and any one of them can approve a job. Self-review is
prevented when an environment gets its first reviewers, unless `--allow-self-review` is given.
After that, the setting changes only when one of the self-review options is given. If you are the
only reviewer and self-review is prevented, you can't approve your own releases; the command warns
about that.

Changing reviewers keeps the environment's other settings, such as a wait timer or deployment
branch policy, and keeps team reviewers added in the GitHub UI unless `--set` or `--clear` replaces
the list. Required reviewers need a public repository or a paid GitHub plan.

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

Then run `setup` again to give the repository a new key. After the revocation, signatures made
with the old key, including earlier ones, are reported as made by a revoked key.

## Development

```shell
go test ./...          # -short skips the gpg compatibility test, which is slow on Windows
go build
```

The tests run against an in-memory fake of the GitHub API and keyserver, so they don't need
network access or a GitHub login. Pushing a `v*` tag builds the release binaries.
