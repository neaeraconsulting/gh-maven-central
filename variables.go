package main

import (
	"flag"
	"fmt"
)

const variablesUsage = `Usage: gh maven-central variables -R OWNER/REPO [options]

Shows or changes the repository variables that the publishing workflows read:
MAVEN_CENTRAL_PUBLISH, which must be true for them to publish, and MAVEN_GROUP_ID, which overrides
the groupId. Without change options, shows them.

Options:
  -R, --repo OWNER/REPO           Repository (required)
` + variableOptionsUsage + `  --dry-run                       Show what would change, without changing it
  -h, --help                      Show this help
`

// variableOptionsUsage is shared by setup and variables
const variableOptionsUsage = `  --publish true|false            Set MAVEN_CENTRAL_PUBLISH
  --remove-publish                Remove MAVEN_CENTRAL_PUBLISH, which turns publishing off
  --group-id GROUP                Set MAVEN_GROUP_ID
  --remove-group-id               Remove MAVEN_GROUP_ID, so the workflows use their default
                                  groupId
`

// variableOptions set or remove the repository variables
type variableOptions struct {
	publish, groupID             string
	removePublish, removeGroupID bool
}

func (o *variableOptions) addFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.publish, "publish", "", "")
	fs.BoolVar(&o.removePublish, "remove-publish", false, "")
	fs.StringVar(&o.groupID, "group-id", "", "")
	fs.BoolVar(&o.removeGroupID, "remove-group-id", false, "")
}

// variableChange sets a variable, or removes it if remove is true
type variableChange struct {
	name, value string
	remove      bool
}

// changes validates the options and returns the changes they ask for
func (o *variableOptions) changes() ([]variableChange, error) {
	var changes []variableChange
	switch {
	case o.publish != "" && o.removePublish:
		return nil, usageErrorf("--publish and --remove-publish can't be combined")
	case o.publish == "true", o.publish == "false":
		changes = append(changes, variableChange{name: publishVariable, value: o.publish})
	case o.publish != "":
		return nil, usageErrorf("--publish must be true or false, not %q", o.publish)
	case o.removePublish:
		changes = append(changes, variableChange{name: publishVariable, remove: true})
	}
	switch {
	case o.groupID != "" && o.removeGroupID:
		return nil, usageErrorf("--group-id and --remove-group-id can't be combined")
	case o.groupID != "" && !groupPattern.MatchString(o.groupID):
		return nil, usageErrorf("--group-id must be a groupId such as io.github.owner, not %q", o.groupID)
	case o.groupID != "":
		changes = append(changes, variableChange{name: groupIDVariable, value: o.groupID})
	case o.removeGroupID:
		changes = append(changes, variableChange{name: groupIDVariable, remove: true})
	}
	return changes, nil
}

func (s *session) changeVariables(changes []variableChange) error {
	for _, c := range changes {
		var err error
		if c.remove {
			err = s.removeVariable(c.name)
		} else {
			err = s.setVariable(c.name, c.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) variables(args []string) error {
	var (
		repo string
		opts variableOptions
		dry  bool
	)
	fs := newFlagSet("variables")
	fs.StringVar(&repo, "R", "", "")
	fs.StringVar(&repo, "repo", "", "")
	opts.addFlags(fs)
	fs.BoolVar(&dry, "dry-run", false, "")
	if err := a.parseFlags(fs, args, variablesUsage); err != nil {
		return err
	}
	if err := checkRepoFlag(repo); err != nil {
		return err
	}
	changes, err := opts.changes()
	if err != nil {
		return err
	}

	s, err := a.newSession(repo, dry)
	if err != nil {
		return err
	}
	// GitHub decides whether the user may read or change the variables
	if _, err := s.readRepo(); err != nil {
		return err
	}
	if len(changes) > 0 {
		return s.changeVariables(changes)
	}
	fmt.Fprintln(s.out, repo)
	for _, name := range []string{publishVariable, groupIDVariable} {
		value, ok, err := s.readVariable(name)
		switch {
		case err != nil:
			return err
		case ok:
			s.info("%s=%s", name, value)
		default:
			s.info("%s is not set", name)
		}
	}
	return nil
}
