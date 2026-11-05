package bench

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Point is one measurement at a context depth.
type Point struct {
	Depth        int     `json:"depth"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	StdDev       float64 `json:"stddev"`
}

// Line is one configuration's measurements, ordered by depth.
type Line struct {
	Label     string  `json:"label"` // "fa on · q8_0"
	FlashAttn string  `json:"flash_attn"`
	CacheK    string  `json:"cache_k"`
	CacheV    string  `json:"cache_v"`
	Backend   string  `json:"backend"`
	NGL       string  `json:"ngl"`
	Points    []Point `json:"points"`
}

// Charts is what the page draws: decode against depth as lines, and prompt
// processing as bars, one entry per configuration in first-seen order.
type Charts struct {
	Decode  []Line `json:"decode"`
	Prefill []Line `json:"prefill"`
}

// Series groups a run's results by configuration. Combined prompt+generation
// tests are left out, as neither chart has a place for them.
func Series(run Run) Charts {
	var c Charts
	for _, r := range run.Results {
		var dst *[]Line
		switch r.Kind {
		case "tg":
			dst = &c.Decode
		case "pp":
			dst = &c.Prefill
		default:
			continue
		}
		i := slices.IndexFunc(*dst, func(l Line) bool { return sameConfig(l, r) })
		if i < 0 {
			*dst = append(*dst, Line{
				Label: configLabel(r), FlashAttn: r.FlashAttn, CacheK: r.CacheK, CacheV: r.CacheV,
				Backend: r.Backend, NGL: r.NGL,
			})
			i = len(*dst) - 1
		}
		(*dst)[i].Points = append((*dst)[i].Points, Point{r.Depth, r.TokensPerSec, r.StdDev})
	}
	for _, lines := range [][]Line{c.Decode, c.Prefill} {
		for _, l := range lines {
			slices.SortStableFunc(l.Points, func(a, b Point) int { return a.Depth - b.Depth })
		}
	}
	return c
}

func sameConfig(l Line, r Result) bool {
	return l.FlashAttn == r.FlashAttn && l.CacheK == r.CacheK && l.CacheV == r.CacheV &&
		l.Backend == r.Backend && l.NGL == r.NGL
}

func configLabel(r Result) string {
	if r.CacheK == r.CacheV {
		return fmt.Sprintf("fa %s · %s", r.FlashAttn, r.CacheK)
	}
	return fmt.Sprintf("fa %s · k %s v %s", r.FlashAttn, r.CacheK, r.CacheV)
}

// Takeaway compares decode speed across configurations at the deepest depth
// they were all measured at, and says nothing else. It returns "" when there
// is nothing to compare.
func Takeaway(run Run) string {
	type entry struct {
		label string
		tps   float64
	}
	lines := Series(run).Decode
	if len(lines) < 2 {
		return ""
	}
	// The deepest depth measured for every configuration.
	common := -1
	for i, l := range lines {
		deepest := l.Points[len(l.Points)-1].Depth
		if i == 0 || deepest < common {
			common = deepest
		}
	}
	var es []entry
	for _, l := range lines {
		for _, p := range l.Points {
			if p.Depth == common {
				es = append(es, entry{l.Label, p.TokensPerSec})
			}
		}
	}
	if len(es) < 2 {
		return ""
	}
	best := slices.MaxFunc(es, func(a, b entry) int { return cmpFloat(a.tps, b.tps) })
	worst := slices.MinFunc(es, func(a, b entry) int { return cmpFloat(a.tps, b.tps) })
	where := "with an empty context"
	if common > 0 {
		where = "at " + depthName(common) + " tokens of context"
	}
	if worst.tps <= 0 || best.tps/worst.tps < 1.01 {
		return fmt.Sprintf("Decode speed %s is within 1%% across the %d configurations.", where, len(es))
	}
	return fmt.Sprintf("Decoding %s, %s reaches %.1f t/s, %.0f%% faster than %s at %.1f t/s.",
		where, best.label, best.tps, (best.tps/worst.tps-1)*100, worst.label, worst.tps)
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func depthName(d int) string {
	if d >= 1024 && d%1024 == 0 {
		return strconv.Itoa(d/1024) + "k"
	}
	return strconv.Itoa(d)
}

// Markdown renders the results as a table, ready to paste into an issue.
func Markdown(run Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` · build `%s`\n\n", run.Setup.ModelFile, run.Build)
	b.WriteString("| Backend | FA | KV (K / V) | Test | t/s |\n| --- | --- | --- | --- | ---: |\n")
	for _, r := range run.Results {
		fmt.Fprintf(&b, "| %s | %s | %s / %s | %s | %.2f ± %.2f |\n",
			r.Backend, r.FlashAttn, r.CacheK, r.CacheV, r.Test, r.TokensPerSec, r.StdDev)
	}
	return b.String()
}

// BuildPoint is the best tg128 at depth 0 of one build, with its change from
// the build before it.
type BuildPoint struct {
	Build        string    `json:"build"`
	RunID        int64     `json:"run_id"`
	StartedAt    time.Time `json:"started_at"`
	TokensPerSec float64   `json:"tokens_per_sec"`
	// DeltaPercent is nil for the first build.
	DeltaPercent *float64 `json:"delta_percent"`
}

// History lists finished runs of model per build, oldest build first. A build
// with several runs shows its best.
func History(runs []Run, model string) []BuildPoint {
	var pts []BuildPoint
	for _, run := range runs {
		if run.State != StateDone || run.Setup.ModelFile != model {
			continue
		}
		best, ok := 0.0, false
		for _, r := range run.Results {
			if r.Kind == "tg" && r.N == 128 && r.Depth == 0 && r.TokensPerSec > best {
				best, ok = r.TokensPerSec, true
			}
		}
		if !ok {
			continue
		}
		i := slices.IndexFunc(pts, func(p BuildPoint) bool { return p.Build == run.Build })
		switch {
		case i < 0:
			pts = append(pts, BuildPoint{run.Build, run.ID, run.StartedAt, best, nil})
		case best > pts[i].TokensPerSec:
			pts[i].TokensPerSec, pts[i].RunID = best, run.ID
		}
		if i >= 0 && run.StartedAt.Before(pts[i].StartedAt) {
			pts[i].StartedAt = run.StartedAt
		}
	}
	slices.SortStableFunc(pts, func(a, b BuildPoint) int { return a.StartedAt.Compare(b.StartedAt) })
	for i := 1; i < len(pts); i++ {
		d := (pts[i].TokensPerSec/pts[i-1].TokensPerSec - 1) * 100
		pts[i].DeltaPercent = &d
	}
	return pts
}
