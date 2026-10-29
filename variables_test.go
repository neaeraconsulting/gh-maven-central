package main

import (
	"slices"
	"strings"
	"testing"
)

func TestVariablesShow(t *testing.T) {
	ta := newTestApp(t)
	ta.gh.admin = false
	ta.gh.variables[publishVariable] = "true"
	if code := ta.run("variables", "-R", "o/r"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	want := "o/r\n    MAVEN_CENTRAL_PUBLISH=true\n    MAVEN_GROUP_ID is not set\n"
	if got := ta.stdout.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("showing changed something: %v", ta.gh.writes)
	}
}

func TestVariablesChange(t *testing.T) {
	ta := newTestApp(t)
	ta.gh.variables[publishVariable] = "true"
	ta.gh.variables[groupIDVariable] = "io.github.o"
	if code := ta.run("variables", "-R", "o/r", "--publish", "false", "--remove-group-id"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	if got := ta.gh.variables; len(got) != 1 || got[publishVariable] != "false" {
		t.Errorf("variables: %v", got)
	}
	if !slices.Contains(ta.gh.writes, "DELETE /repos/o/r/actions/variables/MAVEN_GROUP_ID") {
		t.Errorf("writes: %v", ta.gh.writes)
	}
}

func TestVariablesRemoveUnset(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.run("variables", "-R", "o/r", "--remove-publish"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	if !strings.Contains(ta.stdout.String(), "MAVEN_CENTRAL_PUBLISH is not set") || len(ta.gh.writes) != 0 {
		t.Errorf("output:\n%s\nwrites: %v", ta.stdout, ta.gh.writes)
	}
}

func TestVariablesDryRun(t *testing.T) {
	ta := newTestApp(t)
	ta.gh.variables[groupIDVariable] = "io.github.o"
	if code := ta.run("variables", "-R", "o/r", "--publish", "true", "--remove-group-id", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr)
	}
	for _, want := range []string{"[dry-run] MAVEN_CENTRAL_PUBLISH=true", "[dry-run] Remove MAVEN_GROUP_ID"} {
		if !strings.Contains(ta.stdout.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, ta.stdout)
		}
	}
	if len(ta.gh.writes) != 0 {
		t.Errorf("dry run changed something: %v", ta.gh.writes)
	}
}

func TestVariablesErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no repo", []string{"variables"}, "--repo is required"},
		{"bad publish", []string{"variables", "-R", "o/r", "--publish", "TRUE"}, "--publish must be true or false"},
		{"publish and remove", []string{"variables", "-R", "o/r", "--publish", "true", "--remove-publish"}, "can't be combined"},
		{"group id and remove", []string{"variables", "-R", "o/r", "--group-id", "a.b", "--remove-group-id"}, "can't be combined"},
		{"bad group id", []string{"variables", "-R", "o/r", "--group-id", "a b"}, "--group-id must be a groupId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t)
			if code := ta.run(tt.args...); code != 2 || !strings.Contains(ta.stderr.String(), tt.want) {
				t.Errorf("exit %d, stderr:\n%s", code, ta.stderr)
			}
		})
	}
}
