package main

import (
	"strings"
	"testing"
)

// addStatusRepo sets up a private repository owned by an organization, with variables, repository
// and organization secrets, the release environment with custom branch policies, and a snapshot
// environment
func addStatusRepo(gh *fakeGitHub) {
	gh.private = true
	gh.variables["MAVEN_CENTRAL_PUBLISH"] = "true"
	gh.repoSecrets = []string{"GPG_SIGNING_KEY"}
	gh.orgSecrets = []string{"ORG_TOKEN"}
	addReleaseEnv(gh)
	gh.branchPolicies[release] = []branchPolicy{{"main", "branch"}, {"v*", "tag"}}
	gh.secrets[release]["GPG_SIGNING_KEY"] = "key"
	gh.secrets[release]["CENTRAL_TOKEN_USERNAME"] = "user"
	gh.addEnv(snapshot, map[string]any{
		"deployment_branch_policy": map[string]any{"protected_branches": true, "custom_branch_policies": false},
	})
	gh.envVariables[snapshot]["SNAPSHOT"] = "1"
}

func TestStatus(t *testing.T) {
	ta := newTestApp(t)
	addStatusRepo(ta.gh)
	if code := ta.run("status", "-R", "o/r"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	want := `o/r (private)

Repository variables
    MAVEN_CENTRAL_PUBLISH=true
Repository secrets
    GPG_SIGNING_KEY  updated 2026-10-28
Organization secrets
    ORG_TOKEN  updated 2026-10-28

maven-central-release
    prevent self-review: no
    wait timer: 5 minutes
    reviewers:
      alice (User)
      o/release-team (Team)
    deployment branches: main (branch), v* (tag)
    secrets:
      CENTRAL_TOKEN_PASSWORD    missing
      CENTRAL_TOKEN_USERNAME    updated 2026-10-28
      GPG_SIGNING_KEY           updated 2026-10-28
      GPG_SIGNING_KEY_PASSWORD  missing
    variables:
      (none)

maven-central-snapshot
    prevent self-review: not set
    reviewers:
      (none; jobs run without approval)
    deployment branches: protected branches
    secrets:
      (none)
    variables:
      SNAPSHOT=1
`
	if got := ta.stdout.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	wantWarning := "warning: secret GPG_SIGNING_KEY is set in the repository and in environment " + release +
		"; jobs that don't use the environment get the repository's value\n"
	if got := ta.stderr.String(); got != wantWarning {
		t.Errorf("stderr:\n%s\nwant:\n%s", got, wantWarning)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("status changed something: %v", ta.gh.writes)
	}
}

func TestStatusEnvFilter(t *testing.T) {
	ta := newTestApp(t)
	addStatusRepo(ta.gh)
	ta.gh.orgSecrets = nil
	ta.gh.repoSecrets = nil
	if code := ta.run("status", "-R", "o/r", "--env", snapshot, "--env", "missing"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	out := ta.stdout.String()
	for _, want := range []string{
		"Repository secrets\n    (none)\n\nmaven-central-snapshot\n",
		// A named environment is checked for the secrets even though it has none of them
		"    secrets:\n      CENTRAL_TOKEN_PASSWORD    missing\n",
		"\nmissing\n    doesn't exist\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Organization secrets") {
		t.Errorf("output shows organization secrets for a repository owned by a user:\n%s", out)
	}
	if strings.Contains(out, release) {
		t.Errorf("output shows an environment that wasn't asked for:\n%s", out)
	}
	if ta.stderr.Len() != 0 {
		t.Errorf("stderr:\n%s", ta.stderr)
	}
}

func TestStatusNoEnvironments(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.run("status", "-R", "o/r"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	if out := ta.stdout.String(); !strings.HasPrefix(out, "o/r (public)\n") || !strings.HasSuffix(out, "\nNo environments\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no repo", []string{"status"}, 2, "--repo is required"},
		{"bad env", []string{"status", "-R", "o/r", "--env", "a b"}, 2, "invalid environment name"},
		{"secret name option", []string{"status", "-R", "o/r", "--key-secret", "KEY"}, 2, "flag provided but not defined"},
		{"argument", []string{"status", "-R", "o/r", "extra"}, 2, `unexpected argument "extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			if code := ta.run(tt.args...); code != tt.code || !strings.Contains(ta.stderr.String(), tt.want) {
				t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
			}
		})
	}
}

func TestStatusWithoutAdmin(t *testing.T) {
	ta := newTestApp(t)
	addStatusRepo(ta.gh)
	ta.gh.admin = false
	if code := ta.run("status", "-R", "o/r"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	out := ta.stdout.String()
	cantRead := "can't read: HTTP 403: Must have admin rights to Repository.\n"
	for _, want := range []string{
		"Repository variables\n    MAVEN_CENTRAL_PUBLISH=true\n",
		"Repository secrets\n    " + cantRead,
		"Organization secrets\n    " + cantRead,
		"      alice (User)\n",
		"    deployment branches: main (branch), v* (tag)\n    secrets:\n      " + cantRead,
		"    variables:\n      SNAPSHOT=1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "missing") {
		t.Errorf("output marks secrets missing that it couldn't read:\n%s", out)
	}
	wantWarning := "warning: some settings can't be read; you don't have admin access to o/r, which some of them need\n"
	if got := ta.stderr.String(); got != wantWarning {
		t.Errorf("stderr:\n%s\nwant:\n%s", got, wantWarning)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("status changed something: %v", ta.gh.writes)
	}
}
