package llama

import (
	"context"
	"encoding/json"
	"net/http"
)

// Slot is one entry of GET /slots.
type Slot struct {
	ID            int
	NCtx          int
	IsProcessing  bool
	TaskID        int
	HasTask       bool // id_task stays set after a task finishes
	NPromptTokens int
	NProcessed    int
	NCached       int
	NDecoded      int // next_token[0].n_decoded
	Params        map[string]any
}

func (s *Slot) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID            int            `json:"id"`
		NCtx          int            `json:"n_ctx"`
		IsProcessing  bool           `json:"is_processing"`
		IDTask        *int           `json:"id_task"`
		NPromptTokens int            `json:"n_prompt_tokens"`
		NProcessed    int            `json:"n_prompt_tokens_processed"`
		NCached       int            `json:"n_prompt_tokens_cache"`
		Params        map[string]any `json:"params"`
		NextToken     []struct {
			NDecoded int `json:"n_decoded"`
		} `json:"next_token"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = Slot{
		ID: raw.ID, NCtx: raw.NCtx, IsProcessing: raw.IsProcessing,
		NPromptTokens: raw.NPromptTokens, NProcessed: raw.NProcessed, NCached: raw.NCached,
		Params: raw.Params,
	}
	if raw.IDTask != nil {
		s.TaskID, s.HasTask = *raw.IDTask, true
	}
	if len(raw.NextToken) > 0 {
		s.NDecoded = raw.NextToken[0].NDecoded
	}
	return nil
}

// Tokens splits a slot's context use.
type Tokens struct {
	Cached, Prompt, Generated, Total int
}

// Tokens returns the breakdown of the slot's tokens. While processing,
// n_prompt_tokens is the total and equals cached + prefilled + generated. Once idle
// the per-task counters reset to 0 and n_prompt_tokens is what stays cached.
func (s Slot) Tokens() Tokens {
	if s.IsProcessing {
		return Tokens{Cached: s.NCached, Prompt: s.NProcessed, Generated: s.NDecoded, Total: s.NPromptTokens}
	}
	return Tokens{Cached: s.NPromptTokens, Total: s.NPromptTokens}
}

// Slots lists a loaded model's slots.
func (c *Client) Slots(ctx context.Context, model string) ([]Slot, error) {
	var s []Slot
	if err := c.do(ctx, http.MethodGet, "/slots", modelQuery(model), nil, &s); err != nil {
		return nil, err
	}
	return s, nil
}
