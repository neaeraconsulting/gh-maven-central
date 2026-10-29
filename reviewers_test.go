package main

import (
	"strings"
	"testing"
)

const release = "maven-central-release"

// addReleaseEnv adds an environment with a wait timer, a branch policy, a user and a team
// reviewer, and self-review allowed
func addReleaseEnv(gh *fakeGitHub) {
	gh.addEnv(release, map[string]any{
		"protection_rules": []any{
			map[string]any{"id": 1, "type": "wait_timer", "wait_timer": 5},
			map[string]any{"id": 2, "type": "required_reviewers", "prevent_self_review": false,
				"reviewers": []any{
					map[string]any{"type": "User", "reviewer": map[string]any{"login": "alice", "id": 101}},
					map[string]any{"type": "Team", "reviewer": map[string]any{"slug": "release-team", "id": 202}},
				}},
			map[string]any{"id": 3, "type": "branch_policy"},
		},
		"deployment_branch_policy": map[string]any{"protected_branches": false, "custom_branch_policies": true},
	})
}

const keptSettings = `{"deployment_branch_policy":{"custom_branch_policies":true,"protected_branches":false},"wait_timer":5,`

func TestReviewersQuery(t *testing.T) {
	ta := newTestApp(t)
	addReleaseEnv(ta.gh)
	if code := ta.run("reviewers", "-R", "o/r", "--env", release, "--env", "missing"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	want := `maven-central-release (o/r)
    prevent self-review: no
    wait timer: 5 minutes
    reviewers:
      alice (User)
      o/release-team (Team)
missing (o/r)
    doesn't exist
`
	if got := ta.stdout.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("a query changed something: %v", ta.gh.writes)
	}
}

func TestReviewersChanges(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"add and remove a team by OWNER/slug, ignoring case",
			[]string{"--add", "bob", "--remove", "O/Release-Team"},
			keptSettings + `"reviewers":[{"type":"User","id":101},{"type":"User","id":102}],"prevent_self_review":false}`},
		{"remove a team by slug",
			[]string{"--remove", "release-team"},
			keptSettings + `"reviewers":[{"type":"User","id":101}],"prevent_self_review":false}`},
		{"adding an existing reviewer changes nothing",
			[]string{"--add", "alice"},
			keptSettings + `"reviewers":[{"type":"User","id":101},{"type":"Team","id":202}],"prevent_self_review":false}`},
		{"set replaces the list and keeps the self-review setting",
			[]string{"--set", "carol", "--set", "bob"},
			keptSettings + `"reviewers":[{"type":"User","id":103},{"type":"User","id":102}],"prevent_self_review":false}`},
		{"clear sends an empty list",
			[]string{"--clear"},
			keptSettings + `"reviewers":[]}`},
		{"set the wait timer",
			[]string{"--wait-timer", "10"},
			`{"deployment_branch_policy":{"custom_branch_policies":true,"protected_branches":false},"wait_timer":10,` +
				`"reviewers":[{"type":"User","id":101},{"type":"Team","id":202}],"prevent_self_review":false}`},
		{"remove the wait timer",
			[]string{"--wait-timer", "0"},
			`{"deployment_branch_policy":{"custom_branch_policies":true,"protected_branches":false},"wait_timer":0,` +
				`"reviewers":[{"type":"User","id":101},{"type":"Team","id":202}],"prevent_self_review":false}`},
		{"prevent self-review",
			[]string{"--prevent-self-review"},
			keptSettings + `"reviewers":[{"type":"User","id":101},{"type":"Team","id":202}],"prevent_self_review":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			addReleaseEnv(ta.gh)
			args := append([]string{"reviewers", "-R", "o/r", "--env", release}, tt.args...)
			if code := ta.run(args...); code != 0 {
				t.Fatalf("exit %d: %s", code, ta.stderr)
			}
			want := "PUT /repos/o/r/environments/" + release + " " + tt.want
			if len(ta.gh.writes) != 1 || ta.gh.writes[0] != want {
				t.Errorf("writes:\n%v\nwant:\n%s", strings.Join(ta.gh.writes, "\n"), want)
			}
		})
	}
}

func TestReviewersNewEnvironmentPreventsSelfReview(t *testing.T) {
	ta := newTestApp(t)
	ta.gh.private = true
	if code := ta.run("reviewers", "-R", "o/r", "--env", release, "--add", "alice"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	want := "PUT /repos/o/r/environments/" + release +
		` {"deployment_branch_policy":null,"reviewers":[{"type":"User","id":101}],"prevent_self_review":true}`
	if len(ta.gh.writes) != 1 || ta.gh.writes[0] != want {
		t.Errorf("writes: %v\nwant: %s", ta.gh.writes, want)
	}
	for _, warning := range []string{"is private", "alice is the only reviewer"} {
		if !strings.Contains(ta.stderr.String(), warning) {
			t.Errorf("stderr doesn't warn %q:\n%s", warning, ta.stderr)
		}
	}
}

func TestReviewersDryRun(t *testing.T) {
	ta := newTestApp(t)
	addReleaseEnv(ta.gh)
	if code := ta.run("reviewers", "-R", "o/r", "--env", release, "--add", "bob", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("dry run changed something: %v", ta.gh.writes)
	}
	for _, want := range []string{"[dry-run] Update environment", "would become", "bob (User)"} {
		if !strings.Contains(ta.stdout.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, ta.stdout)
		}
	}
}

func TestReviewersErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no repo", []string{"--env", release}, 2, "--repo is required"},
		{"no env", []string{"-R", "o/r"}, 2, "at least one --env is required"},
		{"set and add", []string{"-R", "o/r", "--env", release, "--set", "bob", "--add", "carol"}, 2, "can't be combined"},
		{"set and clear", []string{"-R", "o/r", "--env", release, "--set", "bob", "--clear"}, 2, "can't be combined"},
		{"allow and prevent", []string{"-R", "o/r", "--env", release, "--allow-self-review", "--prevent-self-review"}, 2, "can't be combined"},
		{"change two envs", []string{"-R", "o/r", "--env", release, "--env", "other", "--add", "bob"}, 2, "exactly one --env"},
		{"negative wait timer", []string{"-R", "o/r", "--env", release, "--wait-timer", "-1"}, 2, "--wait-timer must be from 0"},
		{"bad login", []string{"-R", "o/r", "--env", release, "--add", "a/b"}, 2, "invalid GitHub login"},
		{"unknown option", []string{"-R", "o/r", "--env", release, "--nope"}, 2, "not defined"},
		{"unknown user", []string{"-R", "o/r", "--env", release, "--add", "ghost"}, 1, `GitHub user "ghost" not found`},
		{"organization", []string{"-R", "o/r", "--env", release, "--add", "some-org"}, 1, "is a GitHub Organization, not a user"},
		{"seven reviewers", []string{"-R", "o/r", "--env", release, "--add", "bob", "--add", "carol",
			"--add", "u4", "--add", "u5", "--add", "u6"}, 1, "at most 6 reviewers, not 7"},
		{"unknown repo", []string{"-R", "o/x", "--env", release}, 1, "repository o/x not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			addReleaseEnv(ta.gh)
			for i, login := range []string{"u4", "u5", "u6"} {
				ta.gh.users[login] = map[string]any{"type": "User", "id": 200 + i, "login": login}
			}
			if code := ta.run(append([]string{"reviewers"}, tt.args...)...); code != tt.code {
				t.Errorf("exit %d, want %d", code, tt.code)
			}
			if !strings.Contains(ta.stderr.String(), tt.want) {
				t.Errorf("stderr doesn't contain %q:\n%s", tt.want, ta.stderr)
			}
			if len(ta.gh.writes) != 0 {
				t.Errorf("changed something: %v", ta.gh.writes)
			}
		})
	}
}

func TestReviewersNeedsAdmin(t *testing.T) {
	ta := newTestApp(t)
	ta.gh.admin = false
	if code := ta.run("reviewers", "-R", "o/r", "--env", release); code != 1 ||
		!strings.Contains(ta.stderr.String(), "you need admin access") {
		t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
	}
}
