package main

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

const snapshot = "maven-central-snapshot"

func setupArgs(outDir string, extra ...string) []string {
	return append([]string{"setup", "-R", "o/r",
		"--env", release, "--env", snapshot, "--secrets",
		"--out-dir", outDir}, extra...)
}

const createSnapshot = "PUT /repos/o/r/environments/" + snapshot + ` {"deployment_branch_policy":null,"wait_timer":5}`

func TestSetupWithoutSecrets(t *testing.T) {
	ta := newTestApp(t)
	addReleaseEnv(ta.gh)
	if code := ta.run("setup", "-R", "o/r", "--env", release, "--env", snapshot, "--publish", "true"); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
	}
	want := []string{
		createSnapshot,
		`POST /repos/o/r/actions/variables {"name":"MAVEN_CENTRAL_PUBLISH","value":"true"}`,
	}
	if !slices.Equal(ta.gh.writes, want) {
		t.Errorf("writes: %v\nwant: %v", ta.gh.writes, want)
	}
	if len(ta.ks.uploaded) != 0 || len(ta.term.asked) != 0 {
		t.Errorf("%d keys uploaded, prompts %q", len(ta.ks.uploaded), ta.term.asked)
	}
	if !strings.Contains(ta.stdout.String(), "--env "+release+" --env "+snapshot+" --secrets") {
		t.Errorf("the summary doesn't show how to add the secrets:\n%s", ta.stdout)
	}
}

func TestSetupWaitTimer(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		freePlan bool
		want     string
	}{
		{"custom", []string{"--wait-timer", "30"}, false, ` {"deployment_branch_policy":null,"wait_timer":30}`},
		{"none", []string{"--wait-timer", "0"}, false, ""},
		{"private repository on a free plan", nil, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			ta.gh.private, ta.gh.freePlan = tt.freePlan, tt.freePlan
			args := append([]string{"setup", "-R", "o/r", "--env", snapshot}, tt.args...)
			if code := ta.run(args...); code != 0 {
				t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
			}
			want := "PUT /repos/o/r/environments/" + snapshot + tt.want
			if got := ta.gh.writes[len(ta.gh.writes)-1]; got != want {
				t.Errorf("writes: %v\nwant last: %s", ta.gh.writes, want)
			}
			if tt.freePlan && !strings.Contains(ta.stderr.String(), "creating it without one") {
				t.Errorf("stderr doesn't warn about the wait timer:\n%s", ta.stderr)
			}
		})
	}
}

func TestSetupWaitTimerOnExistingEnvironment(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"default leaves it alone", nil, nil},
		{"same value changes nothing", []string{"--wait-timer", "5"}, nil},
		{"new value keeps the other settings", []string{"--wait-timer", "10"}, []string{"PUT /repos/o/r/environments/" + release +
			` {"deployment_branch_policy":{"custom_branch_policies":true,"protected_branches":false},"wait_timer":10,` +
			`"reviewers":[{"type":"User","id":101},{"type":"Team","id":202}],"prevent_self_review":false}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			addReleaseEnv(ta.gh)
			if code := ta.run(append([]string{"setup", "-R", "o/r", "--env", release}, tt.args...)...); code != 0 {
				t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
			}
			if !slices.Equal(ta.gh.writes, tt.want) {
				t.Errorf("writes: %v\nwant: %v", ta.gh.writes, tt.want)
			}
		})
	}
}

func TestSetupTestSecret(t *testing.T) {
	ta := newTestApp(t)
	addReleaseEnv(ta.gh)
	if code := ta.run("setup", "-R", "o/r", "--env", release, "--env", snapshot, "--test-secret"); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
	}
	for _, env := range []string{release, snapshot} {
		if got := ta.gh.secrets[env]; len(got) != 1 || got[testSecret] != "test" {
			t.Errorf("%s secrets: %v", env, got)
		}
	}
	if len(ta.ks.uploaded) != 0 || len(ta.term.asked) != 0 {
		t.Errorf("%d keys uploaded, prompts %q", len(ta.ks.uploaded), ta.term.asked)
	}

	if code := ta.run("status", "-R", "o/r", "--env", snapshot); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	if want := "TEST_ID                   updated 2026-10-28 (test secret from setup --test-secret)"; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("status doesn't show %q:\n%s", want, ta.stdout)
	}

	ta.gh.secrets[release]["OTHER"] = "kept"
	if code := ta.run("setup", "-R", "o/r", "--env", release, "--env", snapshot, "--remove-test-secret"); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
	}
	if len(ta.gh.secrets[snapshot]) != 0 || len(ta.gh.secrets[release]) != 1 {
		t.Errorf("secrets after removing: %v, %v", ta.gh.secrets[release], ta.gh.secrets[snapshot])
	}
	// Removing it again is a no-op
	ta.gh.writes = nil
	if code := ta.run("setup", "-R", "o/r", "--env", release, "--remove-test-secret"); code != 0 || len(ta.gh.writes) != 0 {
		t.Errorf("exit %d, writes %v", code, ta.gh.writes)
	}
}

func TestSetup(t *testing.T) {
	ta := newTestApp(t)
	addReleaseEnv(ta.gh)
	ta.gh.variables["MAVEN_GROUP_ID"] = "old"
	ta.env["CENTRAL_TOKEN_USERNAME"] = "token-user"
	ta.env["CENTRAL_TOKEN_PASSWORD"] = "token-pass"
	outDir := t.TempDir()

	code := ta.run(setupArgs(outDir,
		"--publish", "true", "--group-id", "io.github.o")...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, ta.stdout, ta.stderr)
	}

	// The existing environment is left alone and the missing one is created with the default wait timer
	for _, w := range ta.gh.writes {
		if strings.HasPrefix(w, "PUT /repos/o/r/environments/"+release+" ") {
			t.Errorf("existing environment was changed: %s", w)
		}
	}
	if !slices.Contains(ta.gh.writes, createSnapshot) {
		t.Errorf("snapshot environment wasn't created: %v", ta.gh.writes)
	}

	// Both environments get the same key, passphrase and token
	rel, snap := ta.gh.secrets[release], ta.gh.secrets[snapshot]
	if len(rel) != 4 || len(snap) != 4 {
		t.Fatalf("secrets: %v, %v", slices.Collect(maps.Keys(rel)), slices.Collect(maps.Keys(snap)))
	}
	for name, value := range rel {
		if snap[name] != value {
			t.Errorf("secret %s differs between the environments", name)
		}
	}
	if rel["CENTRAL_TOKEN_USERNAME"] != "token-user" || rel["CENTRAL_TOKEN_PASSWORD"] != "token-pass" {
		t.Errorf("token secrets: %q %q", rel["CENTRAL_TOKEN_USERNAME"], rel["CENTRAL_TOKEN_PASSWORD"])
	}
	entity := readPrivateKey(t, rel["GPG_SIGNING_KEY"], rel["GPG_SIGNING_KEY_PASSWORD"])
	if got := entity.PrimaryIdentity().Name; got != "r release signing (o/r)" {
		t.Errorf("user ID %q", got)
	}

	// The public key on the keyserver is the one in the secrets
	if len(ta.ks.uploaded) != 1 {
		t.Fatalf("%d keys uploaded", len(ta.ks.uploaded))
	}
	public, err := openpgp.ReadArmoredKeyRing(strings.NewReader(ta.ks.uploaded[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(public[0].PrimaryKey.Fingerprint, entity.PrimaryKey.Fingerprint) {
		t.Error("the uploaded public key isn't the key in the secrets")
	}
	if public[0].PrivateKey != nil {
		t.Error("the uploaded key contains the private key")
	}

	if ta.gh.variables["MAVEN_CENTRAL_PUBLISH"] != "true" || ta.gh.variables["MAVEN_GROUP_ID"] != "io.github.o" {
		t.Errorf("variables: %v", ta.gh.variables)
	}
	if !slices.Contains(ta.gh.writes, `PATCH /repos/o/r/actions/variables/MAVEN_GROUP_ID {"name":"MAVEN_GROUP_ID","value":"io.github.o"}`) ||
		!slices.Contains(ta.gh.writes, `POST /repos/o/r/actions/variables {"name":"MAVEN_CENTRAL_PUBLISH","value":"true"}`) {
		t.Errorf("variables weren't updated and created: %v", ta.gh.writes)
	}

	files, _ := filepath.Glob(filepath.Join(outDir, "o-r-*.rev"))
	if len(files) != 1 {
		t.Fatalf("revocation certificates: %v", files)
	}
	if !strings.Contains(ta.stdout.String(), files[0]) {
		t.Errorf("the summary doesn't show the revocation certificate's path:\n%s", ta.stdout)
	}
}

func TestSetupKeepsToken(t *testing.T) {
	ta := newTestApp(t)
	for _, env := range []string{release, snapshot} {
		ta.gh.addEnv(env, nil)
		ta.gh.secrets[env]["CENTRAL_TOKEN_USERNAME"] = "old-user"
		ta.gh.secrets[env]["CENTRAL_TOKEN_PASSWORD"] = "old-pass"
	}
	ta.term.terminal = true // and a blank username
	if code := ta.run(setupArgs(t.TempDir())...); code != 0 {
		t.Fatalf("exit %d:\n%s", code, ta.stderr)
	}
	if len(ta.term.asked) != 1 || !strings.Contains(ta.term.asked[0], "blank to keep") {
		t.Errorf("prompts: %q", ta.term.asked)
	}
	for _, env := range []string{release, snapshot} {
		if ta.gh.secrets[env]["CENTRAL_TOKEN_USERNAME"] != "old-user" || ta.gh.secrets[env]["GPG_SIGNING_KEY"] == "" {
			t.Errorf("%s secrets: %v", env, ta.gh.secrets[env])
		}
	}
}

func TestSetupAsksForToken(t *testing.T) {
	ta := newTestApp(t)
	ta.term.terminal = true
	ta.term.username, ta.term.password = "typed-user", "typed-pass"
	if code := ta.run(setupArgs(t.TempDir())...); code != 0 {
		t.Fatalf("exit %d:\n%s", code, ta.stderr)
	}
	if len(ta.term.asked) != 2 || strings.Contains(ta.term.asked[0], "blank") {
		t.Errorf("prompts: %q", ta.term.asked)
	}
	if got := ta.gh.secrets[snapshot]["CENTRAL_TOKEN_PASSWORD"]; got != "typed-pass" {
		t.Errorf("token password %q", got)
	}
}

func TestSetupRequiresTokenWhenNoneStored(t *testing.T) {
	ta := newTestApp(t)
	ta.term.terminal = true // and a blank username
	if code := ta.run(setupArgs(t.TempDir())...); code != 1 ||
		!strings.Contains(ta.stderr.String(), "a token username is required") {
		t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
	}
}

func TestSetupNotInteractive(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.run(setupArgs(t.TempDir())...); code != 1 ||
		!strings.Contains(ta.stderr.String(), "set CENTRAL_TOKEN_USERNAME and CENTRAL_TOKEN_PASSWORD") {
		t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("changed something: %v", ta.gh.writes)
	}
}

func TestSetupDryRun(t *testing.T) {
	ta := newTestApp(t)
	outDir := filepath.Join(t.TempDir(), "keys")
	if code := ta.run(setupArgs(outDir, "--remove-group-id", "--publish", "false", "--dry-run")...); code != 0 {
		t.Fatalf("exit %d:\n%s", code, ta.stderr)
	}
	if len(ta.gh.writes) != 0 || len(ta.ks.uploaded) != 0 {
		t.Errorf("dry run changed something: %v, %d keys uploaded", ta.gh.writes, len(ta.ks.uploaded))
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", outDir)
	}
	for _, want := range []string{
		"[dry-run] Ask for the token",
		"[dry-run] Generate an RSA 2048 key \"r release signing (o/r)\"",
		"[dry-run] Upload the public key",
		"[dry-run] Creating environment " + snapshot,
		"[dry-run] " + snapshot + ": CENTRAL_TOKEN_PASSWORD",
		"[dry-run] MAVEN_CENTRAL_PUBLISH=false",
		"MAVEN_GROUP_ID is not set",
		"Dry run finished",
	} {
		if !strings.Contains(ta.stdout.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, ta.stdout)
		}
	}
}

func TestSetupWarnsAboutOutDirInGitWorkTree(t *testing.T) {
	ta := newTestApp(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repo, "keys")
	if code := ta.run(setupArgs(outDir, "--dry-run")...); code != 0 {
		t.Fatalf("exit %d:\n%s", code, ta.stderr)
	}
	if !strings.Contains(ta.stderr.String(), "warning: "+outDir+" is in the git working tree "+repo) {
		t.Errorf("no warning:\n%s", ta.stderr)
	}
}

func TestSetupKeyserverFailureChangesNothingOnGitHub(t *testing.T) {
	ta := newTestApp(t)
	ta.ks.fail = true
	ta.env["CENTRAL_TOKEN_USERNAME"] = "u"
	ta.env["CENTRAL_TOKEN_PASSWORD"] = "p"
	if code := ta.run(setupArgs(t.TempDir())...); code != 1 ||
		!strings.Contains(ta.stderr.String(), "nothing on GitHub was changed") {
		t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("changed something: %v", ta.gh.writes)
	}
}

func TestSetupUsageErrors(t *testing.T) {
	full := setupArgs("dir")[1:]
	without := func(flag string) []string {
		var args []string
		for i := 0; i < len(full); i++ {
			if full[i] == flag {
				i++
				continue
			}
			args = append(args, full[i])
		}
		return args
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no env", without("--env"), "at least one --env is required"},
		{"duplicate env", append(full, "--env", release), "is given twice"},
		{"secret name option", append(full, "--key-secret", "KEY"), "flag provided but not defined"},
		{"test secret and remove", append(full, "--test-secret", "--remove-test-secret"), "can't be combined"},
		{"key option without secrets", []string{"-R", "o/r", "--env", release, "--keyserver", "k"}, "--keyserver needs --secrets"},
		{"bad publish", append(full, "--publish", "yes"), "--publish must be true or false"},
		{"bad group id", append(full, "--group-id", "io..github"), "--group-id must be a groupId"},
		{"wait timer too long", append(full, "--wait-timer", "43201"), "--wait-timer must be from 0 to 43200"},
		{"key name with parenthesis", append(full, "--key-name", "a (b)"), "--key-name can't contain"},
		{"keyserver URL", append(full, "--keyserver", "https://x"), "--keyserver must be a host name"},
		{"extra argument", append(full, "extra"), "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			if code := ta.run(append([]string{"setup"}, tt.args...)...); code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if !strings.Contains(ta.stderr.String(), tt.want) {
				t.Errorf("stderr doesn't contain %q:\n%s", tt.want, ta.stderr)
			}
		})
	}
}

func TestHelpAndUnknownCommand(t *testing.T) {
	ta := newTestApp(t)
	for _, args := range [][]string{{"--help"}, {"setup", "--help"}, {"reviewers", "-h"}} {
		if code := ta.run(args...); code != 0 || !strings.Contains(ta.stdout.String(), "Usage: gh maven-central") {
			t.Errorf("%v: exit %d, output:\n%s", args, code, ta.stdout)
		}
	}
	if code := ta.run("bogus"); code != 2 || !strings.Contains(ta.stderr.String(), `unknown command "bogus"`) {
		t.Errorf("bogus: exit %d, stderr:\n%s", code, ta.stderr)
	}
	if code := ta.run(); code != 2 {
		t.Errorf("no args: exit %d", code)
	}
}

func readPrivateKey(t *testing.T, armored, passphrase string) *openpgp.Entity {
	t.Helper()
	entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		t.Fatal(err)
	}
	entity := entities[0]
	if !entity.PrivateKey.Encrypted {
		t.Fatal("the private key isn't encrypted")
	}
	if err := entity.PrivateKey.Decrypt([]byte(passphrase)); err != nil {
		t.Fatalf("can't decrypt the private key with the passphrase: %v", err)
	}
	return entity
}
