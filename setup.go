package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const setupUsage = `Usage: gh maven-central setup -R OWNER/REPO --env NAME [--env NAME]... [--secrets] [options]

Creates any of the environments that don't exist, with a wait timer, and sets or removes the
repository variables given with the options below. Existing environments' settings and reviewers
are left unchanged; use 'gh maven-central reviewers' to change them.

With --secrets, it also generates a new OpenPGP signing key for the repository, uploads its public
key to a keyserver, and stores the signing key, its passphrase and the Central Portal user token in
every environment as the secrets GPG_SIGNING_KEY, GPG_SIGNING_KEY_PASSWORD, CENTRAL_TOKEN_USERNAME
and CENTRAL_TOKEN_PASSWORD. Running setup --secrets again replaces the key.

Options:
  -R, --repo OWNER/REPO           Repository to set up (required)
  --env NAME                      Deployment environment (required, repeatable)
  --secrets                       Also generate the signing key and store the secrets
  --test-secret                   Store the secret TEST_ID, whose value is just "test", in every
                                  environment, to check that secrets can be stored there
  --remove-test-secret            Remove TEST_ID from every environment
  --wait-timer MINUTES            The wait timer of the environments it creates, which delays
                                  each job that uses them; 0 for none (default: 5)
` + variableOptionsUsage + `  --key-name TEXT                 With --secrets, the name in the key's user ID, which is
                                  "TEXT (OWNER/REPO)" (default: "<repo> release signing")
  --out-dir DIR                   With --secrets, where to save the revocation certificate
                                  (default: ~/.maven-central-keys)
  --keyserver HOST                With --secrets, the keyserver for the public key
                                  (default: keyserver.ubuntu.com)
  --dry-run                       Run the read-only checks and show what would change, without
                                  changing it
  -h, --help                      Show this help

With --secrets, the Central Portal user token is read from the CENTRAL_TOKEN_USERNAME and
CENTRAL_TOKEN_PASSWORD environment variables if set, or else asked for. At the prompt, a blank
username keeps the token already stored in the environments.
`

type setupOptions struct {
	repo                       string
	envs                       stringList
	secrets                    bool
	testSecret, removeTest     bool
	waitTimer                  int
	variables                  variableOptions
	keyName, outDir, keyserver string
	dryRun                     bool
	// given holds the names of the flags on the command line
	given map[string]bool
}

type variable struct{ name, value string }

func (a *app) setup(args []string) error {
	var opts setupOptions
	fs := newFlagSet("setup")
	fs.StringVar(&opts.repo, "R", "", "")
	fs.StringVar(&opts.repo, "repo", "", "")
	fs.Var(&opts.envs, "env", "")
	fs.BoolVar(&opts.secrets, "secrets", false, "")
	fs.BoolVar(&opts.testSecret, "test-secret", false, "")
	fs.BoolVar(&opts.removeTest, "remove-test-secret", false, "")
	fs.IntVar(&opts.waitTimer, "wait-timer", 5, "")
	opts.variables.addFlags(fs)
	fs.StringVar(&opts.keyName, "key-name", "", "")
	fs.StringVar(&opts.outDir, "out-dir", "", "")
	fs.StringVar(&opts.keyserver, "keyserver", "keyserver.ubuntu.com", "")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "")
	if err := a.parseFlags(fs, args, setupUsage); err != nil {
		return err
	}
	opts.given = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { opts.given[f.Name] = true })
	variables, err := opts.check()
	if err != nil {
		return err
	}

	s, err := a.newSession(opts.repo, opts.dryRun)
	if err != nil {
		return err
	}
	if opts.keyName == "" {
		opts.keyName = s.name + " release signing"
	}
	if opts.secrets && opts.outDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("can't find the home directory for --out-dir: %w", err)
		}
		opts.outDir = filepath.Join(home, ".maven-central-keys")
	}

	s.step("Checking %s", opts.repo)
	if err := s.checkRepo(); err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, name := range opts.envs {
		env, err := s.readEnv(name)
		if err != nil {
			return err
		}
		exists[name] = env.Exists
	}

	var (
		token          centralToken
		key            *signingKey
		revocationFile string
	)
	if opts.secrets {
		s.step("Central Portal user token")
		if token, err = s.collectToken(opts); err != nil {
			return err
		}
		s.step("Signing key")
		if key, revocationFile, err = s.makeKey(opts); err != nil {
			return err
		}
		if err := s.uploadPublicKey(opts.keyserver, key); err != nil {
			return fmt.Errorf("%w; nothing on GitHub was changed, so run setup again", err)
		}
	}

	s.step("Environments")
	for _, name := range opts.envs {
		if exists[name] {
			s.info("%s already exists; its settings are unchanged", name)
		} else if err := s.createEnv(name, opts.waitTimer); err != nil {
			return err
		}
	}

	if opts.secrets || opts.testSecret || opts.removeTest {
		s.step("Secrets")
		for _, env := range opts.envs {
			var secrets []variable
			if opts.secrets {
				secrets = append(secrets,
					variable{keySecret, key.ArmoredPrivate},
					variable{passphraseSecret, key.Passphrase})
			}
			if token.username != "" {
				secrets = append(secrets,
					variable{tokenUsernameSecret, token.username},
					variable{tokenPasswordSecret, token.password})
			}
			if opts.testSecret {
				secrets = append(secrets, variable{testSecret, testSecretValue})
			}
			for _, secret := range secrets {
				if err := s.setSecret(env, secret.name, secret.value); err != nil {
					return err
				}
			}
			if opts.removeTest {
				if err := s.removeSecret(env, testSecret); err != nil {
					return err
				}
			}
		}
	}

	if len(variables) > 0 {
		s.step("Repository variables")
		if err := s.changeVariables(variables); err != nil {
			return err
		}
	}

	if s.dryRun {
		s.step("Dry run finished; nothing was changed")
		return nil
	}
	s.step("Done")
	if opts.secrets {
		s.info("Signing key:  %s", key.Fingerprint)
		s.info("Public key:   %s", keyLookupURL(opts.keyserver, key.Fingerprint))
		s.info("Revocation certificate: %s", revocationFile)
		s.info("  Move it somewhere the repository's maintainers can reach, such as a shared password")
		s.info("  manager. It is the only way to revoke this key if the private key leaks.")
	}
	s.info("Next steps:")
	if !opts.secrets {
		s.info("  - Store the signing key and the Central Portal token in the environments with:")
		s.info("      gh maven-central setup -R %s --env %s --secrets", opts.repo, strings.Join(opts.envs, " --env "))
	}
	s.info("  - Check that the groupId namespace is verified in the Central Portal.")
	s.info("  - Add required reviewers to the environments that need approval, such as:")
	s.info("      gh maven-central reviewers -R %s --env %s --set LOGIN", opts.repo, opts.envs[0])
	s.info("  - Check the result with:")
	s.info("      gh maven-central status -R %s", opts.repo)
	return nil
}

// check validates the options and returns the variable changes
func (o *setupOptions) check() ([]variableChange, error) {
	if err := checkRepoFlag(o.repo); err != nil {
		return nil, err
	}
	if err := checkEnvFlags(o.envs); err != nil {
		return nil, err
	}
	variables, err := o.variables.changes()
	if err != nil {
		return nil, err
	}
	if err := checkWaitTimer(o.waitTimer); err != nil {
		return nil, err
	}
	if o.testSecret && o.removeTest {
		return nil, usageErrorf("--test-secret and --remove-test-secret can't be combined")
	}
	if !o.secrets {
		for _, name := range []string{"key-name", "out-dir", "keyserver"} {
			if o.given[name] {
				return nil, usageErrorf("--%s needs --secrets", name)
			}
		}
	}
	if strings.ContainsAny(o.keyName, "()<>") {
		return nil, usageErrorf("--key-name can't contain ( ) < or >")
	}
	if !hostPattern.MatchString(o.keyserver) {
		return nil, usageErrorf("--keyserver must be a host name, such as keyserver.ubuntu.com")
	}
	return variables, nil
}

type centralToken struct {
	// username is empty to keep the token secrets already in the environments
	username, password string
}

func (s *session) collectToken(opts setupOptions) (centralToken, error) {
	if username := s.getenv("CENTRAL_TOKEN_USERNAME"); username != "" {
		password := s.getenv("CENTRAL_TOKEN_PASSWORD")
		if password == "" {
			return centralToken{}, fmt.Errorf("CENTRAL_TOKEN_PASSWORD must be set along with CENTRAL_TOKEN_USERNAME")
		}
		s.info("Using the token from CENTRAL_TOKEN_USERNAME and CENTRAL_TOKEN_PASSWORD")
		return centralToken{username, password}, nil
	}
	canKeep := true
	for _, env := range opts.envs {
		names, err := s.secretNames(env)
		if err != nil {
			return centralToken{}, err
		}
		if !names[tokenUsernameSecret] || !names[tokenPasswordSecret] {
			canKeep = false
		}
	}
	if s.dryRun {
		if canKeep {
			s.info("[dry-run] Ask for the token (blank keeps the token already in every environment)")
		} else {
			s.info("[dry-run] Ask for the token")
		}
		return centralToken{username: "(asked for)"}, nil
	}
	if !s.terminal.IsTerminal() {
		return centralToken{}, fmt.Errorf("set CENTRAL_TOKEN_USERNAME and CENTRAL_TOKEN_PASSWORD, or run in a " +
			"terminal (in Git Bash's mintty, use Windows Terminal, PowerShell or winpty instead)")
	}
	s.info("Generate a user token in the Central Portal: https://central.sonatype.com/usertoken")
	prompt := "    Token username: "
	if canKeep {
		prompt = "    Token username (blank to keep the existing token): "
	}
	username, err := s.terminal.ReadLine(prompt)
	if err != nil {
		return centralToken{}, err
	}
	if username == "" {
		if !canKeep {
			return centralToken{}, fmt.Errorf("a token username is required, because the environments don't have a token yet")
		}
		s.info("Keeping the existing token")
		return centralToken{}, nil
	}
	password, err := s.terminal.ReadPassword("    Token password: ")
	if err != nil {
		return centralToken{}, err
	}
	if password == "" {
		return centralToken{}, fmt.Errorf("a token password is required")
	}
	return centralToken{username, password}, nil
}

// makeKey generates the key and saves its revocation certificate, returning the certificate's path
func (s *session) makeKey(opts setupOptions) (*signingKey, string, error) {
	uid := fmt.Sprintf("%s (%s)", opts.keyName, opts.repo)
	if root := gitWorkTree(opts.outDir); root != "" {
		s.warn("%s is in the git working tree %s; don't commit the revocation certificate", opts.outDir, root)
	}
	if s.dryRun {
		s.info("[dry-run] Generate an RSA %d key \"%s\"", s.rsaBits, uid)
		s.info("[dry-run] Save the revocation certificate in %s", opts.outDir)
		return &signingKey{Fingerprint: strings.Repeat("0", 40)}, "", nil
	}
	s.info("Generating an RSA %d key \"%s\"", s.rsaBits, uid)
	key, err := generateKey(opts.keyName, opts.repo, s.rsaBits, s.now())
	if err != nil {
		return nil, "", err
	}
	s.info("Fingerprint: %s", key.Fingerprint)
	if err := os.MkdirAll(opts.outDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("can't create %s: %w", opts.outDir, err)
	}
	path := filepath.Join(opts.outDir, fmt.Sprintf("%s-%s-%s.rev", s.owner, s.name, key.keyID()))
	if err := os.WriteFile(path, []byte(key.Revocation), 0o600); err != nil {
		return nil, "", fmt.Errorf("can't save the revocation certificate: %w", err)
	}
	s.info("Saved the revocation certificate as %s", path)
	return key, path, nil
}

// gitWorkTree returns the root of the git working tree containing dir, or "" if there isn't one
func gitWorkTree(dir string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		// .git is a file in linked worktrees and submodules
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
