package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	zedSystemID  = "6b87ab66-af2c-49c7-b986-ef4c27c9e1fb"
	zedVersion   = "0.222.4+stable.147.b385025df963c9e8c3f74cc4dadb1c4b29b3c6f0"
	zedTokenURL  = "https://cloud.zed.dev/client/llm_tokens"
	zedComplURL  = "https://cloud.zed.dev/completions"
	zedModelsURL = "https://cloud.zed.dev/models"
	zedUsersURL  = "https://cloud.zed.dev/client/users/me"
)

// proxyOnce ensures proxy detection runs once.
var (
	proxyOnce sync.Once
	proxyURL  *url.URL
)

func initProxy() {
	proxyOnce.Do(func() {
		for _, env := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			val := os.Getenv(env)
			if val == "" {
				continue
			}
			u, err := url.Parse(val)
			if err == nil && u.Host != "" {
				proxyURL = u
				fmt.Printf("[zed2api] proxy: %s\n", u.Host)
				return
			}
		}
		fmt.Println("[zed2api] proxy: none (set HTTPS_PROXY to use)")
	})
}

// newHTTPClient returns an http.Client with optional proxy support.
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

// ── Token management ──

// getToken returns a valid JWT for the account, refreshing if needed.
func getToken(acc *Account) (string, error) {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if acc.jwtToken != "" && time.Now().Unix() < acc.jwtExp-60 {
		return acc.jwtToken, nil
	}
	tok, exp, err := fetchNewToken(acc)
	if err != nil {
		return "", err
	}
	acc.jwtToken = tok
	acc.jwtExp = exp
	fmt.Printf("[zed] token refreshed for uid %s\n", acc.UserID)
	return tok, nil
}

func fetchNewToken(acc *Account) (string, int64, error) {
	authHeader := acc.UserID + " " + acc.CredentialJSON

	client := newHTTPClient(30 * time.Second)
	req, err := http.NewRequest("POST", zedTokenURL, strings.NewReader(""))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("authorization", authHeader)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-zed-system-id", zedSystemID)

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token fetch: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("token fetch status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", 0, fmt.Errorf("token parse: %w", err)
	}
	if result.Token == "" {
		return "", 0, fmt.Errorf("empty token in response")
	}

	exp := parseJWTExp(result.Token)
	return result.Token, exp, nil
}

// parseJWTExp decodes the JWT payload and returns the exp claim.
func parseJWTExp(jwt string) int64 {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return 0
	}
	payload := parts[1]
	// add padding
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return 0
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return 0
	}
	return claims.Exp
}

// ── Upstream HTTP ──

// doZedRequest sends a POST to Zed completions endpoint and returns the body.
func doZedRequest(jwt string, payload []byte) ([]byte, int, error) {
	client := newHTTPClient(120 * time.Second)
	req, err := http.NewRequest("POST", zedComplURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("authorization", "Bearer "+jwt)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-zed-version", zedVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// doZedRequestStream opens a streaming POST to Zed and returns the response for reading.
// Caller is responsible for closing resp.Body.
func doZedRequestStream(jwt string, payload []byte) (*http.Response, error) {
	client := newHTTPClient(300 * time.Second)
	req, err := http.NewRequest("POST", zedComplURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+jwt)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-zed-version", zedVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("[zed] stream error %d: %s\n", resp.StatusCode, truncate(string(b), 200))
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	return resp, nil
}

// ── Models ──

func fetchModels(acc *Account) ([]byte, error) {
	jwt, err := getToken(acc)
	if err != nil {
		return nil, err
	}
	client := newHTTPClient(15 * time.Second)
	req, err := http.NewRequest("GET", zedModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+jwt)
	req.Header.Set("x-zed-version", zedVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models fetch status %d", resp.StatusCode)
	}
	return body, nil
}

// ── Billing/Usage ──

func fetchBillingUsage(acc *Account) ([]byte, error) {
	authHeader := acc.UserID + " " + acc.CredentialJSON
	client := newHTTPClient(15 * time.Second)
	req, err := http.NewRequest("GET", zedUsersURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", authHeader)
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("users/me status %d: %s", resp.StatusCode, body)
	}
	return body, nil
}
