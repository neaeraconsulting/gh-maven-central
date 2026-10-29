package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"golang.org/x/crypto/nacl/box"
)

// fakeGitHub is an in-memory GitHub REST API with just the endpoints this tool uses. Tests talk to
// it through a real go-gh client, so go-gh's error handling is exercised too.
type fakeGitHub struct {
	t       *testing.T
	mu      sync.Mutex
	admin   bool
	private bool
	// envs holds each environment's GET response
	envs  map[string]map[string]any
	users map[string]map[string]any
	// secrets holds the decrypted secrets of each environment
	secrets        map[string]map[string]string
	keys           map[string]*[32]byte
	variables      map[string]string
	envVariables   map[string]map[string]string
	branchPolicies map[string][]branchPolicy
	repoSecrets    []string
	// orgSecrets is nil for a repository owned by a user.  GitHub answers 422 when asked for those.
	orgSecrets []string
	// writes records each request that changes something, as "METHOD path body"
	writes []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	return &fakeGitHub{
		t:     t,
		admin: true,
		envs:  map[string]map[string]any{},
		users: map[string]map[string]any{
			"alice":    {"type": "User", "id": 101, "login": "alice"},
			"bob":      {"type": "User", "id": 102, "login": "bob"},
			"carol":    {"type": "User", "id": 103, "login": "carol"},
			"some-org": {"type": "Organization", "id": 900, "login": "some-org"},
		},
		secrets:        map[string]map[string]string{},
		keys:           map[string]*[32]byte{},
		variables:      map[string]string{},
		envVariables:   map[string]map[string]string{},
		branchPolicies: map[string][]branchPolicy{},
	}
}

// addEnv adds an environment with the given GET response fields
func (f *fakeGitHub) addEnv(name string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["name"] = name
	if _, ok := fields["protection_rules"]; !ok {
		fields["protection_rules"] = []any{}
	}
	if _, ok := fields["deployment_branch_policy"]; !ok {
		fields["deployment_branch_policy"] = nil
	}
	f.envs[name] = fields
	f.secrets[name] = map[string]string{}
	f.envVariables[name] = map[string]string{}
}

func (f *fakeGitHub) client() requester {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		f.serve(rec, req)
		resp := rec.Result()
		// go-gh's error handling reads the request
		resp.Request = req
		return resp, nil
	})
	client, err := api.NewRESTClient(api.ClientOptions{
		Host: "github.com", AuthToken: "test", Transport: transport,
		// Skips looking up the local time zone for every client
		Headers: map[string]string{"Time-Zone": "UTC"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (f *fakeGitHub) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	path, _ := url.PathUnescape(req.URL.Path)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if req.Method != http.MethodGet {
		f.writes = append(f.writes, strings.TrimSpace(req.Method+" "+path+" "+string(body)))
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	notFound := func() { reply(http.StatusNotFound, map[string]string{"message": "Not Found"}) }

	switch {
	case len(parts) == 2 && parts[0] == "users":
		if user, ok := f.users[parts[1]]; ok {
			reply(http.StatusOK, user)
		} else {
			notFound()
		}
	case len(parts) == 3 && parts[0] == "repos" && parts[1]+"/"+parts[2] == "o/r":
		owner := "User"
		if f.orgSecrets != nil {
			owner = "Organization"
		}
		reply(http.StatusOK, map[string]any{"private": f.private, "owner": map[string]string{"type": owner},
			"permissions": map[string]bool{"admin": f.admin}})
	case len(parts) < 3 || parts[0] != "repos" || parts[1]+"/"+parts[2] != "o/r":
		notFound()
	case len(parts) == 4 && parts[3] == "environments":
		var list []any
		for _, env := range f.envs {
			list = append(list, env)
		}
		reply(http.StatusOK, map[string]any{"total_count": len(list), "environments": list})
	case len(parts) == 5 && parts[3] == "environments":
		name := parts[4]
		switch req.Method {
		case http.MethodGet:
			if env, ok := f.envs[name]; ok {
				reply(http.StatusOK, env)
			} else {
				notFound()
			}
		case http.MethodPut:
			if _, ok := f.envs[name]; !ok {
				f.addEnv(name, nil)
			}
			reply(http.StatusOK, f.envs[name])
		}
	case len(parts) >= 6 && parts[3] == "environments" && parts[5] == "secrets":
		f.serveSecrets(parts[4], parts[6:], req.Method, body, reply, notFound)
	case len(parts) == 6 && parts[3] == "environments" && parts[5] == "variables":
		if _, ok := f.envs[parts[4]]; ok {
			reply(http.StatusOK, map[string]any{"variables": variableList(f.envVariables[parts[4]])})
		} else {
			notFound()
		}
	case len(parts) == 6 && parts[3] == "environments" && parts[5] == "deployment-branch-policies":
		reply(http.StatusOK, map[string]any{"branch_policies": f.branchPolicies[parts[4]]})
	case len(parts) == 5 && parts[3] == "actions" && parts[4] == "secrets":
		reply(http.StatusOK, map[string]any{"secrets": secretList(f.repoSecrets)})
	case len(parts) == 5 && parts[3] == "actions" && parts[4] == "organization-secrets":
		if f.orgSecrets != nil {
			reply(http.StatusOK, map[string]any{"secrets": secretList(f.orgSecrets)})
		} else {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		}
	case len(parts) >= 5 && parts[3] == "actions" && parts[4] == "variables":
		switch {
		case len(parts) == 5 && req.Method == http.MethodGet:
			reply(http.StatusOK, map[string]any{"variables": variableList(f.variables)})
		case len(parts) == 6 && req.Method == http.MethodGet:
			if value, ok := f.variables[parts[5]]; ok {
				reply(http.StatusOK, map[string]string{"name": parts[5], "value": value})
			} else {
				notFound()
			}
		case len(parts) == 6 && req.Method == http.MethodPatch, len(parts) == 5 && req.Method == http.MethodPost:
			var v struct{ Name, Value string }
			_ = json.Unmarshal(body, &v)
			f.variables[v.Name] = v.Value
			if req.Method == http.MethodPost {
				reply(http.StatusCreated, map[string]any{})
			} else {
				reply(http.StatusNoContent, nil)
			}
		default:
			notFound()
		}
	default:
		notFound()
	}
}

func (f *fakeGitHub) serveSecrets(env string, rest []string, method string, body []byte,
	reply func(int, any), notFound func()) {
	if _, ok := f.envs[env]; !ok {
		notFound()
		return
	}
	switch {
	case len(rest) == 0 && method == http.MethodGet:
		list := secretList(slices.Collect(maps.Keys(f.secrets[env])))
		reply(http.StatusOK, map[string]any{"total_count": len(list), "secrets": list})
	case len(rest) == 1 && rest[0] == "public-key":
		if f.keys[env] == nil {
			pub, priv, err := box.GenerateKey(rand.Reader)
			if err != nil {
				f.t.Fatal(err)
			}
			f.keys[env+"/public"] = pub
			f.keys[env] = priv
		}
		reply(http.StatusOK, map[string]string{
			"key_id": "key-" + env, "key": base64.StdEncoding.EncodeToString(f.keys[env+"/public"][:])})
	case len(rest) == 1 && method == http.MethodPut:
		var v struct {
			EncryptedValue string `json:"encrypted_value"`
			KeyID          string `json:"key_id"`
		}
		_ = json.Unmarshal(body, &v)
		if v.KeyID != "key-"+env {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "wrong key_id"})
			return
		}
		sealed, _ := base64.StdEncoding.DecodeString(v.EncryptedValue)
		value, ok := box.OpenAnonymous(nil, sealed, f.keys[env+"/public"], f.keys[env])
		if !ok {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "can't decrypt"})
			return
		}
		f.secrets[env][rest[0]] = string(value)
		// GitHub answers 201 with an empty body when a secret is created
		reply(http.StatusCreated, nil)
	default:
		notFound()
	}
}

// secretList is a list of secrets as GitHub returns it, all updated at fakeSecretTime
func secretList(names []string) []map[string]string {
	var list []map[string]string
	for _, name := range names {
		list = append(list, map[string]string{"name": name, "updated_at": fakeSecretTime})
	}
	return list
}

const fakeSecretTime = "2026-10-28T12:00:00Z"

func variableList(variables map[string]string) []map[string]string {
	var list []map[string]string
	for name, value := range variables {
		list = append(list, map[string]string{"name": name, "value": value})
	}
	return list
}

// fakeKeyserver is an HKP keyserver that stores uploaded keys
type fakeKeyserver struct {
	server   *httptest.Server
	mu       sync.Mutex
	uploaded []string
	fail     bool
}

func newFakeKeyserver(t *testing.T) *fakeKeyserver {
	k := &fakeKeyserver{}
	k.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		switch req.URL.Path {
		case "/pks/add":
			if k.fail {
				http.Error(w, "nope", http.StatusInternalServerError)
				return
			}
			_ = req.ParseForm()
			k.uploaded = append(k.uploaded, req.PostForm.Get("keytext"))
		case "/pks/lookup":
			if len(k.uploaded) == 0 {
				http.NotFound(w, req)
				return
			}
			_, _ = io.WriteString(w, k.uploaded[len(k.uploaded)-1])
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(k.server.Close)
	return k
}

type fakeTerminal struct {
	terminal bool
	username string
	password string
	asked    []string
}

func (t *fakeTerminal) IsTerminal() bool { return t.terminal }

func (t *fakeTerminal) ReadLine(prompt string) (string, error) {
	t.asked = append(t.asked, prompt)
	return t.username, nil
}

func (t *fakeTerminal) ReadPassword(prompt string) (string, error) {
	t.asked = append(t.asked, prompt)
	return t.password, nil
}

// testApp is an app wired to the fakes, with its output captured
type testApp struct {
	*app
	gh     *fakeGitHub
	ks     *fakeKeyserver
	term   *fakeTerminal
	env    map[string]string
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newTestApp(t *testing.T) *testApp {
	ta := &testApp{
		gh:     newFakeGitHub(t),
		ks:     newFakeKeyserver(t),
		term:   &fakeTerminal{},
		env:    map[string]string{},
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	ta.app = &app{
		out:          ta.stdout,
		errOut:       ta.stderr,
		newAPI:       func() (requester, error) { return ta.gh.client(), nil },
		httpClient:   ta.ks.server.Client(),
		keyserverURL: func(string) string { return ta.ks.server.URL },
		getenv:       func(name string) string { return ta.env[name] },
		terminal:     ta.term,
		// Smaller keys keep the tests fast; TestGenerateKey checks 4096-bit keys
		rsaBits:      2048,
		now:          time.Now,
		pollAttempts: 2,
		pollDelay:    time.Millisecond,
	}
	return ta
}

func (ta *testApp) run(args ...string) int {
	ta.stdout.Reset()
	ta.stderr.Reset()
	return run(args, ta.app)
}
