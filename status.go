package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

const statusUsage = `Usage: gh maven-central status -R OWNER/REPO [--env NAME]...

Shows how a repository is set up for publishing: its variables and secret names, the organization
secrets it can use, and each environment's reviewers, wait timer, deployment branches, secret names
and variables. Secret values are never shown. Anything your gh login can't read, such as secrets
without admin access, is marked as such.

Marks the secrets that setup --secrets stores as missing in the environments that have any of
them or the TEST_ID secret from setup --test-secret, and in the environments given with --env.
Warns about secrets that are set both in an environment
and in the repository or organization, because jobs that don't use the environment get the
repository's or organization's value.

Options:
  -R, --repo OWNER/REPO    Repository (required)
  --env NAME               Environment to show (repeatable; default: all of them)
  -h, --help               Show this help
`

func (a *app) status(args []string) error {
	var (
		repo string
		envs stringList
	)
	fs := newFlagSet("status")
	fs.StringVar(&repo, "R", "", "")
	fs.StringVar(&repo, "repo", "", "")
	fs.Var(&envs, "env", "")
	if err := a.parseFlags(fs, args, statusUsage); err != nil {
		return err
	}
	if err := checkRepoFlag(repo); err != nil {
		return err
	}
	if len(envs) > 0 {
		if err := checkEnvFlags(envs); err != nil {
			return err
		}
	}
	s, err := a.newSession(repo, false)
	if err != nil {
		return err
	}
	// Unlike setup and reviewers, status shows whatever the user can read without admin access
	admin, err := s.readRepo()
	if err != nil {
		return err
	}
	visibility := "public"
	if s.private {
		visibility = "private"
	}
	fmt.Fprintf(s.out, "%s (%s)\n\n", repo, visibility)
	unreadable := false
	cantRead := func(prefix string, err error) {
		unreadable = true
		s.info("%scan't read: %s", prefix, readError(err))
	}

	fmt.Fprintln(s.out, "Repository variables")
	if variables, err := s.listVariables("repos/" + repo + "/actions/variables"); err != nil {
		cantRead("", err)
	} else {
		s.printVariables("", variables)
	}
	fmt.Fprintln(s.out, "Repository secrets")
	repoSecrets, err := s.listSecrets("repos/" + repo + "/actions/secrets")
	if err != nil {
		cantRead("", err)
	} else {
		s.printSecrets("", repoSecrets, nil)
	}
	var orgSecrets []secretInfo
	if s.orgOwned {
		fmt.Fprintln(s.out, "Organization secrets")
		if orgSecrets, err = s.listSecrets("repos/" + repo + "/actions/organization-secrets"); err != nil {
			cantRead("", err)
		} else {
			s.printSecrets("", orgSecrets, nil)
		}
	}

	named := len(envs) > 0
	if !named {
		names, err := s.listEnvNames()
		switch {
		case err != nil:
			fmt.Fprintln(s.out, "\nEnvironments")
			cantRead("", err)
		case len(names) == 0:
			fmt.Fprintln(s.out, "\nNo environments")
		}
		slices.Sort(names)
		envs = names
	}
	var warnings []string
	for _, name := range envs {
		fmt.Fprintln(s.out)
		env, err := s.readEnv(name)
		if err != nil {
			fmt.Fprintln(s.out, name)
			cantRead("", err)
			continue
		}
		if !env.Exists {
			fmt.Fprintln(s.out, name)
			s.info("doesn't exist")
			continue
		}
		s.printEnv(name, env, env.Reviewers, env.PreventSelfReview)
		if err := s.printBranches(env); err != nil {
			cantRead("deployment branches: ", err)
		}
		s.info("secrets:")
		if secrets, err := s.listSecrets(s.envPath(name) + "/secrets"); err != nil {
			cantRead("  ", err)
		} else {
			var expected []string
			if named || slices.ContainsFunc(secrets, func(secret secretInfo) bool {
				return secret.Name == testSecret || slices.Contains(publishingSecrets, secret.Name)
			}) {
				expected = publishingSecrets
			}
			s.printSecrets("  ", secrets, expected)
			warnings = append(warnings, overlaps(name, secrets, repoSecrets, "repository")...)
			warnings = append(warnings, overlaps(name, secrets, orgSecrets, "organization")...)
		}
		s.info("variables:")
		if variables, err := s.listVariables(s.envPath(name) + "/variables"); err != nil {
			cantRead("  ", err)
		} else {
			s.printVariables("  ", variables)
		}
	}
	for _, w := range warnings {
		s.warn("%s", w)
	}
	switch {
	case unreadable && !admin:
		s.warn("some settings can't be read; you don't have admin access to %s, which some of them need", repo)
	case unreadable:
		s.warn("some settings can't be read")
	}
	return nil
}

func (s *session) printBranches(env *environment) error {
	var policy struct {
		ProtectedBranches    bool `json:"protected_branches"`
		CustomBranchPolicies bool `json:"custom_branch_policies"`
	}
	if err := json.Unmarshal(env.BranchPolicy, &policy); err != nil {
		return fmt.Errorf("can't read the deployment branch policy of environment %s: %w", env.Name, err)
	}
	switch {
	case policy.ProtectedBranches:
		s.info("deployment branches: protected branches")
	case policy.CustomBranchPolicies:
		policies, err := s.branchPolicies(env.Name)
		if err != nil {
			return err
		}
		var list []string
		for _, p := range policies {
			list = append(list, fmt.Sprintf("%s (%s)", p.Name, p.Type))
		}
		if len(list) == 0 {
			list = []string{"none"}
		}
		s.info("deployment branches: %s", strings.Join(list, ", "))
	default:
		s.info("deployment branches: all")
	}
	return nil
}

// printSecrets lists the secrets by name, and the expected ones that are missing
func (s *session) printSecrets(indent string, secrets []secretInfo, expected []string) {
	notes := map[string]string{}
	for _, secret := range secrets {
		notes[secret.Name] = "updated " + secret.UpdatedAt.UTC().Format(time.DateOnly)
		if secret.Name == testSecret {
			notes[secret.Name] += " (test secret from setup --test-secret)"
		}
	}
	for _, name := range expected {
		if _, ok := notes[name]; !ok {
			notes[name] = "missing"
		}
	}
	if len(notes) == 0 {
		s.info("%s(none)", indent)
		return
	}
	names := slices.Sorted(maps.Keys(notes))
	width := len(slices.MaxFunc(names, func(a, b string) int { return len(a) - len(b) }))
	for _, name := range names {
		s.info("%s%-*s  %s", indent, width, name, notes[name])
	}
}

func (s *session) printVariables(indent string, variables []variable) {
	if len(variables) == 0 {
		s.info("%s(none)", indent)
		return
	}
	slices.SortFunc(variables, func(a, b variable) int { return strings.Compare(a.name, b.name) })
	for _, v := range variables {
		s.info("%s%s=%s", indent, v.name, v.value)
	}
}

// overlaps returns warnings for the environment's secrets that are also set at a broader level
func overlaps(env string, secrets, broader []secretInfo, level string) []string {
	var warnings []string
	for _, secret := range secrets {
		if slices.ContainsFunc(broader, func(b secretInfo) bool { return b.Name == secret.Name }) {
			warnings = append(warnings, fmt.Sprintf("secret %s is set in the %s and in environment %s; "+
				"jobs that don't use the environment get the %s's value", secret.Name, level, env, level))
		}
	}
	return warnings
}
