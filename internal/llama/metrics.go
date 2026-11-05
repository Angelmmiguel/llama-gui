package llama

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ParseMetrics parses Prometheus text into name to value, dropping the llamacpp: prefix.
// Comments, labelled series and unparseable lines are skipped.
func ParseMetrics(r io.Reader) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 || strings.ContainsAny(f[0], "{}") {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		out[strings.TrimPrefix(f[0], "llamacpp:")] = v
	}
	return out
}

// Metrics returns a loaded model's metrics.
func (c *Client) Metrics(ctx context.Context, model string) (map[string]float64, error) {
	b, err := c.raw(ctx, http.MethodGet, "/metrics", modelQuery(model), nil)
	if err != nil {
		return nil, err
	}
	return ParseMetrics(bytes.NewReader(b)), nil
}
