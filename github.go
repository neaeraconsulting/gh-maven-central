package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"golang.org/x/crypto/nacl/box"
)

// requester is the part of the go-gh REST client this tool uses. Request is used rather than Do,
// because Do fails on the empty bodies of some successful responses, such as creating a secret.
type requester interface {
	Request(method string, path string, body io.Reader) (*http.Response, error)
}

// call sends in as the JSON request body (none if nil) and decodes the response into out (if not nil)
func (s *session) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := s.api.Request(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func isNotFound(err error) bool {
	httpErr, ok := errors.AsType[*api.HTTPError](err)
	return ok && httpErr.StatusCode == http.StatusNotFound
}

func (s *session) envPath(env string) string {
	return fmt.Sprintf("repos/%s/environments/%s", s.repo, url.PathEscape(env))
}

// checkRepo checks that the user can administer the repository, and records whether it's private
// and whether an organization owns it
func (s *session) checkRepo() error {
	admin, err := s.readRepo()
	if err != nil {
		return err
	}
	if !admin {
		return fmt.Errorf("you need admin access to %s to manage its environments and secrets", s.repo)
	}
	return nil
}

// readRepo records whether the repository is private and whether an organization owns it, and
// returns whether the user can administer it
func (s *session) readRepo() (bool, error) {
	var repo struct {
		Private bool `json:"private"`
		Owner   struct {
			Type string `json:"type"`
		} `json:"owner"`
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := s.call(http.MethodGet, "repos/"+s.repo, nil, &repo); err != nil {
		if isNotFound(err) {
			return false, fmt.Errorf("repository %s not found, or not visible to your gh login", s.repo)
		}
		return false, fmt.Errorf("can't read repository %s: %w", s.repo, err)
	}
	s.private = repo.Private
	s.orgOwned = repo.Owner.Type == "Organization"
	return repo.Permissions.Admin, nil
}

// readError describes why a read failed, without the request URL that go-gh adds
func readError(err error) string {
	httpErr, ok := errors.AsType[*api.HTTPError](err)
	switch {
	case !ok:
		return err.Error()
	case httpErr.Message == "":
		return fmt.Sprintf("HTTP %d", httpErr.StatusCode)
	default:
		return fmt.Sprintf("HTTP %d: %s", httpErr.StatusCode, httpErr.Message)
	}
}

// environment is the state of a deployment environment that this tool reads or preserves
type environment struct {
	Name              string
	Exists            bool
	PreventSelfReview *bool
	WaitTimer         int
	// BranchPolicy is the deployment_branch_policy JSON, sent back unchanged on updates
	BranchPolicy json.RawMessage
	Reviewers    []reviewer
}

type reviewer struct {
	Type string // User or Team
	ID   int64
	// Name is a user's login or a team's slug
	Name string
}

// label is a user's login or a team's OWNER/slug
func (r reviewer) label(owner string) string {
	if r.Type == "Team" {
		return owner + "/" + r.Name
	}
	return r.Name
}

func (s *session) readEnv(name string) (*environment, error) {
	var resp struct {
		ProtectionRules []struct {
			Type              string `json:"type"`
			WaitTimer         int    `json:"wait_timer"`
			PreventSelfReview *bool  `json:"prevent_self_review"`
			Reviewers         []struct {
				Type     string `json:"type"`
				Reviewer struct {
					ID    int64  `json:"id"`
					Login string `json:"login"`
					Slug  string `json:"slug"`
				} `json:"reviewer"`
			} `json:"reviewers"`
		} `json:"protection_rules"`
		DeploymentBranchPolicy json.RawMessage `json:"deployment_branch_policy"`
	}
	env := &environment{Name: name, BranchPolicy: json.RawMessage("null")}
	if err := s.call(http.MethodGet, s.envPath(name), nil, &resp); err != nil {
		if isNotFound(err) {
			return env, nil
		}
		return nil, fmt.Errorf("can't read environment %s: %w", name, err)
	}
	env.Exists = true
	if len(resp.DeploymentBranchPolicy) > 0 {
		env.BranchPolicy = resp.DeploymentBranchPolicy
	}
	for _, rule := range resp.ProtectionRules {
		switch rule.Type {
		case "wait_timer":
			env.WaitTimer = rule.WaitTimer
		case "required_reviewers":
			env.PreventSelfReview = rule.PreventSelfReview
			for _, r := range rule.Reviewers {
				name := r.Reviewer.Login
				if r.Type == "Team" {
					name = r.Reviewer.Slug
				}
				env.Reviewers = append(env.Reviewers, reviewer{Type: r.Type, ID: r.Reviewer.ID, Name: name})
			}
		}
	}
	return env, nil
}

type reviewerRef struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

// envUpdate is the body of PUT repos/{repo}/environments/{name}
type envUpdate struct {
	DeploymentBranchPolicy json.RawMessage `json:"deployment_branch_policy"`
	WaitTimer              *int            `json:"wait_timer,omitempty"`
	Reviewers              *[]reviewerRef  `json:"reviewers,omitempty"`
	PreventSelfReview      *bool           `json:"prevent_self_review,omitempty"`
}

func (s *session) putEnv(name string, update envUpdate) error {
	b, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if s.dryRun {
		s.info("[dry-run] Update environment %s: %s", name, b)
		return nil
	}
	s.info("Updating environment %s", name)
	if err := s.call(http.MethodPut, s.envPath(name), update, nil); err != nil {
		return fmt.Errorf("can't update environment %s: %w", name, err)
	}
	return nil
}

func (s *session) createEnv(name string) error {
	return s.write("Creating environment "+name, func() error {
		if err := s.call(http.MethodPut, s.envPath(name), nil, nil); err != nil {
			return fmt.Errorf("can't create environment %s: %w", name, err)
		}
		return nil
	})
}

func (s *session) resolveUser(login string) (reviewer, error) {
	var user struct {
		Type  string `json:"type"`
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := s.call(http.MethodGet, "users/"+url.PathEscape(login), nil, &user); err != nil {
		if isNotFound(err) {
			return reviewer{}, fmt.Errorf("GitHub user %q not found", login)
		}
		return reviewer{}, fmt.Errorf("can't look up GitHub user %q: %w", login, err)
	}
	if user.Type != "User" {
		return reviewer{}, fmt.Errorf("%q is a GitHub %s, not a user; only users can be added as reviewers", login, user.Type)
	}
	return reviewer{Type: "User", ID: user.ID, Name: user.Login}, nil
}

// secretNames returns the names of the environment's secrets; none if it doesn't exist
func (s *session) secretNames(env string) (map[string]bool, error) {
	names := map[string]bool{}
	secrets, err := s.listSecrets(s.envPath(env) + "/secrets")
	if isNotFound(err) {
		return names, nil
	}
	if err != nil {
		return nil, fmt.Errorf("can't list the secrets of environment %s: %w", env, err)
	}
	for _, secret := range secrets {
		names[secret.Name] = true
	}
	return names, nil
}

type secretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

// listSecrets lists the secrets at path, such as an environment's secrets, sorted by name
func (s *session) listSecrets(path string) ([]secretInfo, error) {
	var resp struct {
		Secrets []secretInfo `json:"secrets"`
	}
	if err := s.call(http.MethodGet, path+"?per_page=100", nil, &resp); err != nil {
		return nil, err
	}
	slices.SortFunc(resp.Secrets, func(a, b secretInfo) int { return strings.Compare(a.Name, b.Name) })
	return resp.Secrets, nil
}

// listVariables lists the variables at path, such as an environment's variables
func (s *session) listVariables(path string) ([]variable, error) {
	var resp struct {
		Variables []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"variables"`
	}
	if err := s.call(http.MethodGet, path+"?per_page=100", nil, &resp); err != nil {
		return nil, err
	}
	var list []variable
	for _, v := range resp.Variables {
		list = append(list, variable{v.Name, v.Value})
	}
	return list, nil
}

func (s *session) listEnvNames() ([]string, error) {
	var resp struct {
		Environments []struct {
			Name string `json:"name"`
		} `json:"environments"`
	}
	if err := s.call(http.MethodGet, "repos/"+s.repo+"/environments?per_page=100", nil, &resp); err != nil {
		return nil, fmt.Errorf("can't list the environments of %s: %w", s.repo, err)
	}
	var names []string
	for _, env := range resp.Environments {
		names = append(names, env.Name)
	}
	return names, nil
}

type branchPolicy struct {
	Name string `json:"name"`
	Type string `json:"type"` // branch or tag
}

// branchPolicies lists the environment's custom deployment branch and tag patterns
func (s *session) branchPolicies(env string) ([]branchPolicy, error) {
	var resp struct {
		BranchPolicies []branchPolicy `json:"branch_policies"`
	}
	if err := s.call(http.MethodGet, s.envPath(env)+"/deployment-branch-policies?per_page=100", nil, &resp); err != nil {
		return nil, fmt.Errorf("can't list the deployment branch policies of environment %s: %w", env, err)
	}
	return resp.BranchPolicies, nil
}

// setSecret encrypts value with the environment's public key and stores it as secret name
func (s *session) setSecret(env, name, value string) error {
	return s.write(env+": "+name, func() error {
		var key struct {
			KeyID string `json:"key_id"`
			Key   string `json:"key"`
		}
		if err := s.call(http.MethodGet, s.envPath(env)+"/secrets/public-key", nil, &key); err != nil {
			return fmt.Errorf("can't get the secrets public key of environment %s: %w", env, err)
		}
		encrypted, err := sealSecret(key.Key, value)
		if err != nil {
			return fmt.Errorf("can't encrypt secret %s: %w", name, err)
		}
		body := map[string]string{"encrypted_value": encrypted, "key_id": key.KeyID}
		if err := s.call(http.MethodPut, s.envPath(env)+"/secrets/"+name, body, nil); err != nil {
			return fmt.Errorf("can't set secret %s in environment %s: %w", name, env, err)
		}
		return nil
	})
}

func (s *session) removeSecret(env, name string) error {
	names, err := s.secretNames(env)
	if err != nil {
		return err
	}
	if !names[name] {
		s.info("%s: %s is not set", env, name)
		return nil
	}
	return s.write(env+": remove "+name, func() error {
		if err := s.call(http.MethodDelete, s.envPath(env)+"/secrets/"+name, nil, nil); err != nil {
			return fmt.Errorf("can't remove secret %s from environment %s: %w", name, env, err)
		}
		return nil
	})
}

// sealSecret encrypts value for GitHub with a libsodium sealed box, given the base64 public key
func sealSecret(publicKey, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("public key is %d bytes, not 32", len(raw))
	}
	var recipient [32]byte
	copy(recipient[:], raw)
	sealed, err := box.SealAnonymous(nil, []byte(value), &recipient, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// setVariable creates or updates a repository variable
func (s *session) setVariable(name, value string) error {
	return s.write(name+"="+value, func() error {
		path := "repos/" + s.repo + "/actions/variables"
		body := map[string]string{"name": name, "value": value}
		err := s.call(http.MethodGet, path+"/"+name, nil, nil)
		switch {
		case err == nil:
			err = s.call(http.MethodPatch, path+"/"+name, body, nil)
		case isNotFound(err):
			err = s.call(http.MethodPost, path, body, nil)
		}
		if err != nil {
			return fmt.Errorf("can't set variable %s: %w", name, err)
		}
		return nil
	})
}

// readVariable returns a repository variable's value and whether it's set
func (s *session) readVariable(name string) (string, bool, error) {
	var v struct {
		Value string `json:"value"`
	}
	err := s.call(http.MethodGet, "repos/"+s.repo+"/actions/variables/"+name, nil, &v)
	switch {
	case isNotFound(err):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("can't read variable %s: %w", name, err)
	}
	return v.Value, true, nil
}

func (s *session) removeVariable(name string) error {
	_, ok, err := s.readVariable(name)
	if err != nil {
		return err
	}
	if !ok {
		s.info("%s is not set", name)
		return nil
	}
	return s.write("Remove "+name, func() error {
		if err := s.call(http.MethodDelete, "repos/"+s.repo+"/actions/variables/"+name, nil, nil); err != nil {
			return fmt.Errorf("can't remove variable %s: %w", name, err)
		}
		return nil
	})
}
