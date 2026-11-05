package llama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// Device is one memory-accounting device of a model.
type Device struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Model   int64  `json:"model"`
	Context int64  `json:"context"`
	Compute int64  `json:"compute"`
}

// MemoryEstimate is the memory use at one context size.
type MemoryEstimate struct {
	NCtx    int      `json:"n_ctx"`
	Devices []Device `json:"devices"`
}

// Memory holds the router's memory measurement; Effective is zero while unloaded.
type Memory struct {
	Status    string         `json:"status"`
	Min       MemoryEstimate `json:"min"`
	Max       MemoryEstimate `json:"max"`
	Effective MemoryEstimate `json:"effective"`
}

// Model is one entry of GET /models.
type Model struct {
	ID            string
	Status        string
	Args          []string
	Port          int // from the child's --port; 0 when unloaded or absent
	ExitCode      int
	Failed        bool
	ContextLength int
	Source        string
	Memory        Memory
}

func (m *Model) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID     string `json:"id"`
		Status struct {
			Value    string   `json:"value"`
			Args     []string `json:"args"`
			ExitCode int      `json:"exit_code"`
			Failed   bool     `json:"failed"`
		} `json:"status"`
		ContextLength int    `json:"context_length"`
		Source        string `json:"source"`
		Memory        Memory `json:"memory"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Model{
		ID: raw.ID, Status: raw.Status.Value, Args: raw.Status.Args,
		ExitCode: raw.Status.ExitCode, Failed: raw.Status.Failed,
		ContextLength: raw.ContextLength, Source: raw.Source, Memory: raw.Memory,
	}
	m.Port = PortFromArgs(m.Args)
	return nil
}

// PortFromArgs returns the value following --port, or 0.
func PortFromArgs(args []string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--port" {
			p, _ := strconv.Atoi(args[i+1])
			return p
		}
	}
	return 0
}

// Reload makes the router re-read its preset and returns the resulting models.
func (c *Client) Reload(ctx context.Context) ([]Model, error) {
	var r struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", url.Values{"reload": {"1"}}, nil, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Models lists the router's models.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var r struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, nil, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}
