// Command gh-maven-central is a gh CLI extension that sets up a GitHub repository to publish to
// Maven Central from GitHub Actions. It generates a signing key for the repository, stores it and
// a Central Portal token as secrets in deployment environments, sets repository variables, and
// manages the environments' required reviewers.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const mainUsage = `Usage: gh maven-central <command> [options]

Sets up a GitHub repository to publish to Maven Central from GitHub Actions.

Commands:
  setup       Generate a signing key and store it and a Central Portal token as secrets in
              deployment environments, and set repository variables
  reviewers   Show or change the required reviewers of deployment environments

Run 'gh maven-central <command> --help' for the options of each command.
`

// usageError is an error in the command line, as opposed to one while running the command
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

func main() {
	os.Exit(run(os.Args[1:], newApp()))
}

// run runs the command line args and returns the exit code
func run(args []string, a *app) int {
	if len(args) == 0 {
		fmt.Fprint(a.errOut, mainUsage)
		return 2
	}
	var err error
	switch args[0] {
	case "setup":
		err = a.setup(args[1:])
	case "reviewers":
		err = a.reviewers(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(a.out, mainUsage)
		return 0
	default:
		fmt.Fprint(a.errOut, mainUsage)
		fmt.Fprintf(a.errOut, "error: unknown command %q\n", args[0])
		return 2
	}
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, new(usageError)):
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 2
	default:
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
}
