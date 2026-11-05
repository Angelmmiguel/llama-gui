// Package llama is a typed, tolerant client for llama-server's router API.
// Unknown fields are ignored and missing ones become zero values.
package llama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrUpstream is returned for any non-2xx answer from llama-server.
type ErrUpstream struct {
	Status  int
	Message string
}

func (e *ErrUpstream) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("llama-server: status %d", e.Status)
	}
	return fmt.Sprintf("llama-server: status %d: %s", e.Status, e.Message)
}

// Client talks to one router.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for the router at baseURL. A nil httpClient uses http.DefaultClient.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), hc: httpClient}
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	data, err := c.raw(ctx, method, path, q, body)
	if err != nil || out == nil || len(bytes.TrimSpace(data)) == 0 {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, method, path string, q url.Values, body any) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &ErrUpstream{Status: resp.StatusCode, Message: errorMessage(data)}
	}
	return data, nil
}

// errorMessage extracts error.message, accepting a bare string error or plain text.
func errorMessage(data []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && len(e.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			return s
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func modelQuery(model string) url.Values {
	return url.Values{"model": {model}, "autoload": {"false"}}
}

// Health returns nil when /health answers 2xx.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil, nil)
}

// Load asks the router to load a model.
func (c *Client) Load(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/models/load", nil, map[string]string{"model": id}, nil)
}

// Unload asks the router to unload a model.
func (c *Client) Unload(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/models/unload", nil, map[string]string{"model": id}, nil)
}

// Props returns the raw /props document of a loaded model.
func (c *Client) Props(ctx context.Context, model string) (map[string]any, error) {
	var m map[string]any
	err := c.do(ctx, http.MethodGet, "/props", modelQuery(model), nil, &m)
	return m, err
}

// SlotAction runs save, restore or erase on a slot. filename is only used by save and restore.
func (c *Client) SlotAction(ctx context.Context, model string, slot int, action, filename string) (map[string]any, error) {
	body := map[string]string{"model": model}
	if filename != "" {
		body["filename"] = filename
	}
	var m map[string]any
	q := url.Values{"action": {action}, "autoload": {"false"}}
	err := c.do(ctx, http.MethodPost, "/slots/"+strconv.Itoa(slot), q, body, &m)
	return m, err
}
