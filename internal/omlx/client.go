// SPDX-License-Identifier: GPL-3.0-or-later

// Package omlx is a minimal client for a local oMLX server's
// OpenAI-compatible chat endpoint. Same conventions as the other local
// tools: OMLX_URL / OMLX_API_KEY env overrides, .env fallback, key never
// logged.
package omlx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultBaseURL = "http://127.0.0.1:8000/v1"

type Client struct {
	BaseURL string
	Model   string
	apiKey  string
	HTTP    *http.Client
}

// New resolves base URL and API key, each as: explicit arg > env var >
// ./.env > ~/.env > default (OMLX_URL, OMLX_API_KEY).
func New(model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = resolveVar("OMLX_URL")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		apiKey:  resolveVar("OMLX_API_KEY"),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

func resolveVar(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{".env", filepath.Join(home, ".env")} {
		if v := keyFromEnvFile(p, name); v != "" {
			return v
		}
	}
	return ""
}

func keyFromEnvFile(path, name string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "export ")
		if val, ok := strings.CutPrefix(line, name+"="); ok {
			val = strings.TrimSuffix(strings.TrimSpace(val), "\r")
			return strings.Trim(val, `"'`)
		}
	}
	return ""
}

type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []chatMessage  `json:"messages"`
	Temperature        float64        `json:"temperature"`
	Stream             bool           `json:"stream"`
	MaxTokens          int            `json:"max_tokens,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Chat sends one system+user exchange and returns the assistant text.
func (c *Client) Chat(system, user string) (string, error) {
	return c.ChatMax(system, user, 512)
}

// ChatMax is Chat with an explicit max_tokens — batch prompts scale it with
// the batch size so long replies are never cut off mid-verdict.
func (c *Client) ChatMax(system, user string, maxTokens int) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       c.Model,
		Messages:    []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: 0,
		Stream:      false,
		MaxTokens:   maxTokens,
		// Qwen3-style thinking off — we only want the verdict.
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("oMLX unreachable at %s (start it with \"omlx serve\"): %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", fmt.Errorf("oMLX rejected the request (401) — set OMLX_API_KEY")
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("oMLX HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("parse oMLX response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("oMLX returned no choices")
	}
	return cr.Choices[0].Message.Content, nil
}

// Models lists the model IDs the server offers (also a cheap health check).
func (c *Client) Models() ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oMLX unreachable at %s (start it with \"omlx serve\", or set OMLX_URL): %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("oMLX rejected the request (401) — set OMLX_API_KEY")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oMLX HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse oMLX model list: %w", err)
	}
	ids := make([]string, len(list.Data))
	for i, m := range list.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
