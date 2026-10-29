package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// uploadPublicKey uploads the key to the keyserver with the HKP protocol, and waits until the
// keyserver returns it
func (s *session) uploadPublicKey(host string, key *signingKey) error {
	if s.dryRun {
		s.info("[dry-run] Upload the public key to %s", host)
		return nil
	}
	base := s.keyserverURL(host)
	s.info("Uploading the public key to %s", host)
	resp, err := s.httpClient.PostForm(base+"/pks/add", url.Values{"keytext": {key.ArmoredPublic}})
	if err != nil {
		return fmt.Errorf("upload to %s failed: %w", host, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload to %s failed: %s %s", host, resp.Status, strings.TrimSpace(string(body)))
	}
	for attempt := 1; attempt <= s.pollAttempts; attempt++ {
		if s.keyserverHas(base, key.Fingerprint) {
			s.info("The keyserver returns the key")
			return nil
		}
		if attempt < s.pollAttempts {
			s.info("Waiting for the keyserver to return the key (%d/%d)", attempt, s.pollAttempts)
			time.Sleep(s.pollDelay)
		}
	}
	return fmt.Errorf("%s doesn't return the uploaded key", host)
}

func (s *session) keyserverHas(base, fingerprint string) bool {
	resp, err := s.httpClient.Get(base + "/pks/lookup?op=get&options=mr&search=0x" + fingerprint)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return err == nil && resp.StatusCode == http.StatusOK &&
		strings.Contains(string(body), "BEGIN PGP PUBLIC KEY BLOCK")
}

func keyLookupURL(host, fingerprint string) string {
	return "https://" + host + "/pks/lookup?op=get&search=0x" + fingerprint
}
