package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"golang.org/x/term"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// Secret and variable names
	namePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	hostPattern  = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
)

// app holds the dependencies of the commands, so tests can replace them
type app struct {
	out, errOut io.Writer
	newAPI      func() (requester, error)
	httpClient  *http.Client
	// keyserverURL turns the --keyserver host into the keyserver's base URL
	keyserverURL func(host string) string
	getenv       func(string) string
	terminal     terminal
	rsaBits      int
	now          func() time.Time
	pollAttempts int
	pollDelay    time.Duration
}

// terminal reads the Central Portal token from the user
type terminal interface {
	IsTerminal() bool
	ReadLine(prompt string) (string, error)
	ReadPassword(prompt string) (string, error)
}

func newApp() *app {
	return &app{
		out:    os.Stdout,
		errOut: os.Stderr,
		newAPI: func() (requester, error) {
			// Setting Time-Zone skips go-gh's lookup of the local time zone, which can take
			// seconds on Windows. It only affects how GitHub formats some times, which this
			// tool doesn't use.
			return api.NewRESTClient(api.ClientOptions{Headers: map[string]string{"Time-Zone": "UTC"}})
		},
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		keyserverURL: func(host string) string { return "https://" + host },
		getenv:       os.Getenv,
		terminal:     &stdinTerminal{reader: bufio.NewReader(os.Stdin)},
		rsaBits:      4096,
		now:          time.Now,
		pollAttempts: 6,
		pollDelay:    10 * time.Second,
	}
}

type stdinTerminal struct {
	reader *bufio.Reader
}

func (t *stdinTerminal) IsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func (t *stdinTerminal) ReadLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := t.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (t *stdinTerminal) ReadPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(b)), err
}

// session is one command run against one repository
type session struct {
	*app
	api     requester
	repo    string
	owner   string
	name    string
	dryRun  bool
	private bool
}

func (a *app) newSession(repo string, dryRun bool) (*session, error) {
	client, err := a.newAPI()
	if err != nil {
		return nil, fmt.Errorf("can't connect to GitHub (log in with 'gh auth login'): %w", err)
	}
	owner, name, _ := strings.Cut(repo, "/")
	return &session{app: a, api: client, repo: repo, owner: owner, name: name, dryRun: dryRun}, nil
}

func (s *session) step(format string, args ...any) {
	fmt.Fprintf(s.out, "\n==> "+format+"\n", args...)
}

func (s *session) info(format string, args ...any) {
	fmt.Fprintf(s.out, "    "+format+"\n", args...)
}

func (s *session) warn(format string, args ...any) {
	fmt.Fprintf(s.errOut, "warning: "+format+"\n", args...)
}

// write runs fn, which changes something, or with --dry-run only describes it
func (s *session) write(description string, fn func() error) error {
	if s.dryRun {
		s.info("[dry-run] %s", description)
		return nil
	}
	s.info("%s", description)
	return fn()
}

// stringList is a repeatable string flag
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseFlags parses args, printing usage for --help or an error
func (a *app) parseFlags(fs *flag.FlagSet, args []string, usage string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(a.out, usage)
			return err
		}
		fmt.Fprint(a.errOut, usage)
		return usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		fmt.Fprint(a.errOut, usage)
		return usageErrorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func checkRepoFlag(repo string) error {
	if repo == "" {
		return usageErrorf("--repo is required")
	}
	if !repoPattern.MatchString(repo) {
		return usageErrorf("--repo must be OWNER/REPO, not %q", repo)
	}
	return nil
}

func checkEnvFlags(envs []string) error {
	if len(envs) == 0 {
		return usageErrorf("at least one --env is required")
	}
	seen := map[string]bool{}
	for _, env := range envs {
		if !envPattern.MatchString(env) {
			return usageErrorf("invalid environment name %q", env)
		}
		if seen[strings.ToLower(env)] {
			return usageErrorf("--env %s is given twice", env)
		}
		seen[strings.ToLower(env)] = true
	}
	return nil
}

func checkName(flagName, value string) error {
	if value == "" {
		return usageErrorf("%s is required", flagName)
	}
	if !namePattern.MatchString(value) || strings.HasPrefix(strings.ToUpper(value), "GITHUB_") {
		return usageErrorf("%s: invalid name %q (letters, digits and _, not starting with a digit or GITHUB_)", flagName, value)
	}
	return nil
}
