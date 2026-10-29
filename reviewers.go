package main

import (
	"fmt"
	"strings"
)

// GitHub allows up to 6 required reviewers per environment
const maxReviewers = 6

const reviewersUsage = `Usage: gh maven-central reviewers -R OWNER/REPO --env NAME [--env NAME]...
       gh maven-central reviewers -R OWNER/REPO --env NAME [change options]

With no change options, shows the required reviewers of each environment. Otherwise changes the
reviewers of one environment, creating it if it doesn't exist. The environment's other settings,
such as a wait timer or deployment branch policy, are kept.

Options:
  -R, --repo OWNER/REPO   Repository (required)
  --env NAME              Deployment environment (required; repeatable to show several)
  --dry-run               Show the result without changing anything
  -h, --help              Show this help

Change options:
  --add LOGIN             Add a user as a reviewer (repeatable)
  --remove LOGIN          Remove a user, or a team by slug or OWNER/slug (repeatable)
  --set LOGIN             Replace all reviewers with these users (repeatable)
  --clear                 Remove all reviewers, so jobs run without approval
  --allow-self-review     Let the person who triggered a job approve it
  --prevent-self-review   Don't let the person who triggered a job approve it. If neither option
                          is given, the current setting is kept, and an environment that has no
                          reviewers yet prevents self-review.
`

func (a *app) reviewers(args []string) error {
	var (
		repo                       string
		envs, adds, removes, sets  stringList
		clear, allow, prevent, dry bool
	)
	fs := newFlagSet("reviewers")
	fs.StringVar(&repo, "R", "", "")
	fs.StringVar(&repo, "repo", "", "")
	fs.Var(&envs, "env", "")
	fs.Var(&adds, "add", "")
	fs.Var(&removes, "remove", "")
	fs.Var(&sets, "set", "")
	fs.BoolVar(&clear, "clear", false, "")
	fs.BoolVar(&allow, "allow-self-review", false, "")
	fs.BoolVar(&prevent, "prevent-self-review", false, "")
	fs.BoolVar(&dry, "dry-run", false, "")
	if err := a.parseFlags(fs, args, reviewersUsage); err != nil {
		return err
	}
	if err := checkRepoFlag(repo); err != nil {
		return err
	}
	if err := checkEnvFlags(envs); err != nil {
		return err
	}
	if allow && prevent {
		return usageErrorf("--allow-self-review and --prevent-self-review can't be combined")
	}
	replace := len(sets) > 0 || clear
	if replace && (len(adds) > 0 || len(removes) > 0 || (len(sets) > 0 && clear)) {
		return usageErrorf("--set and --clear can't be combined with each other or with --add or --remove")
	}
	changing := replace || len(adds) > 0 || len(removes) > 0 || allow || prevent
	if changing && len(envs) != 1 {
		return usageErrorf("changing reviewers needs exactly one --env")
	}
	for _, login := range append(append([]string{}, sets...), adds...) {
		if !loginPattern.MatchString(login) {
			return usageErrorf("invalid GitHub login %q", login)
		}
	}
	if len(sets) > maxReviewers {
		return usageErrorf("an environment can have at most %d reviewers", maxReviewers)
	}

	s, err := a.newSession(repo, dry)
	if err != nil {
		return err
	}
	if err := s.checkRepo(); err != nil {
		return err
	}

	if !changing {
		for _, name := range envs {
			env, err := s.readEnv(name)
			if err != nil {
				return err
			}
			if env.Exists {
				s.printEnv(fmt.Sprintf("%s (%s)", name, repo), env, env.Reviewers, env.PreventSelfReview)
			} else {
				fmt.Fprintf(s.out, "%s (%s)\n", name, repo)
				s.info("doesn't exist")
			}
		}
		return nil
	}

	env, err := s.readEnv(envs[0])
	if err != nil {
		return err
	}
	if !env.Exists {
		s.info("%s doesn't exist yet, so it will be created", env.Name)
	}
	list := append([]reviewer{}, env.Reviewers...)
	if replace {
		list = nil
	}
	for _, name := range removes {
		var removed bool
		if list, removed = removeReviewer(list, name, s.owner); !removed {
			s.warn("%s is not a reviewer, so it wasn't removed", name)
		}
	}
	for _, login := range append(append([]string{}, sets...), adds...) {
		r, err := s.resolveUser(login)
		if err != nil {
			return err
		}
		var added bool
		if list, added = addReviewer(list, r); !added {
			s.info("%s is already a reviewer", r.Name)
		}
	}
	preventSetting := decidePrevent(selfReviewFlag(allow, prevent), env.PreventSelfReview, list)
	if err := s.checkReviewers(list, preventSetting); err != nil {
		return err
	}
	if err := s.putEnv(env.Name, updateBody(env, list, preventSetting)); err != nil {
		return err
	}
	heading := fmt.Sprintf("%s (%s)", env.Name, repo)
	if dry {
		heading += " would become"
	}
	s.printEnv(heading, env, list, preventSetting)
	return nil
}

// selfReviewFlag returns the prevent_self_review value the options ask for, or nil if neither
func selfReviewFlag(allow, prevent bool) *bool {
	switch {
	case allow:
		return new(false)
	case prevent:
		return new(true)
	default:
		return nil
	}
}

func ptr[T any](v T) *T { return &v }

// addReviewer adds r unless it's already in the list
func addReviewer(list []reviewer, r reviewer) ([]reviewer, bool) {
	for _, existing := range list {
		if existing.Type == r.Type && existing.ID == r.ID {
			return list, false
		}
	}
	return append(list, r), true
}

// removeReviewer removes the user, or the team by slug or OWNER/slug, named name (ignoring case)
func removeReviewer(list []reviewer, name, owner string) ([]reviewer, bool) {
	for i, r := range list {
		if strings.EqualFold(r.Name, name) || strings.EqualFold(r.label(owner), name) {
			return append(list[:i:i], list[i+1:]...), true
		}
	}
	return list, false
}

// decidePrevent returns the prevent_self_review value to send: the option if given, otherwise the
// current setting, otherwise true. It returns nil if there are no reviewers to apply it to.
func decidePrevent(option, current *bool, reviewers []reviewer) *bool {
	switch {
	case option != nil:
		return option
	case len(reviewers) == 0:
		return nil
	case current != nil:
		return current
	default:
		return new(true)
	}
}

func (s *session) checkReviewers(list []reviewer, prevent *bool) error {
	if len(list) > maxReviewers {
		return fmt.Errorf("an environment can have at most %d reviewers, not %d", maxReviewers, len(list))
	}
	if len(list) > 0 && s.private {
		s.warn("%s is private: required reviewers on private repositories need a paid GitHub plan", s.repo)
	}
	if prevent != nil && *prevent && len(list) == 1 && list[0].Type == "User" {
		s.warn("%s is the only reviewer and can't approve jobs they triggered themselves; "+
			"add another reviewer or pass --allow-self-review", list[0].Name)
	}
	return nil
}

// updateBody returns the PUT body that sets the reviewers and keeps env's other settings, so a PUT
// never resets settings made in the GitHub UI
func updateBody(env *environment, list []reviewer, prevent *bool) envUpdate {
	update := envUpdate{DeploymentBranchPolicy: env.BranchPolicy, PreventSelfReview: prevent}
	if env.WaitTimer > 0 {
		update.WaitTimer = new(env.WaitTimer)
	}
	// An empty list is sent only to remove existing reviewers
	if len(list) > 0 || len(env.Reviewers) > 0 {
		refs := make([]reviewerRef, 0, len(list))
		for _, r := range list {
			refs = append(refs, reviewerRef{Type: r.Type, ID: r.ID})
		}
		update.Reviewers = &refs
	}
	return update
}

func (s *session) printEnv(heading string, env *environment, list []reviewer, prevent *bool) {
	fmt.Fprintln(s.out, heading)
	switch {
	case prevent == nil:
		s.info("prevent self-review: not set")
	case *prevent:
		s.info("prevent self-review: yes")
	default:
		s.info("prevent self-review: no")
	}
	if env.WaitTimer > 0 {
		s.info("wait timer: %d minutes", env.WaitTimer)
	}
	s.info("reviewers:")
	if len(list) == 0 {
		s.info("  (none; jobs run without approval)")
	}
	for _, r := range list {
		s.info("  %s (%s)", r.label(s.owner), r.Type)
	}
}
