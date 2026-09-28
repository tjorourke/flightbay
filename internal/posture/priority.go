package posture

import (
	"math"
	"sort"
	"strings"
)

// Priority is one problem to fix, ranked by the score it would give back.
type Priority struct {
	Rank     int      `json:"rank"`
	ID       string   `json:"id"`
	Area     string   `json:"area"`
	Title    string   `json:"title"`
	Severity string   `json:"severity"`
	Status   Status   `json:"status"`
	Points   float64  `json:"points"` // area score points recovered if every object passed
	Impact   float64  `json:"impact"` // severity weight x share missing, the ranking key
	Failing  int      `json:"failing"`
	Warning  int      `json:"warning"`
	Total    int      `json:"total"`
	Objects  []string `json:"objects"` // the first few objects to change
	Fix      string   `json:"fix"`
	Source   string   `json:"source"`
	Docs     string   `json:"docs,omitempty"`
}

// PointsIfFixed is the number of area score points a check would recover if
// every object it looked at passed: its severity weight times the credit it
// is missing, as a share of the area's total weight.
func PointsIfFixed(r Report, c Check) float64 {
	if c.Status == NA || c.Status == Pass {
		return 0
	}
	var total float64
	for _, x := range r.Checks {
		if x.Area == c.Area && x.Status != NA {
			total += weight[x.Severity]
		}
	}
	if total == 0 {
		return 0
	}
	p := 100 * weight[c.Severity] * (1 - c.Score) / total
	return math.Round(p*10) / 10
}

// Impact is how much a problem matters regardless of which area it is in:
// severity weight times the share of credit missing. Ranking by area points
// alone would bury a critical check in a large area (agentic has many checks,
// so each is worth fewer of its points) under a high check in a small one.
func Impact(c Check) float64 {
	if c.Status == NA || c.Status == Pass {
		return 0
	}
	return weight[c.Severity] * (1 - c.Score)
}

// Priorities ranks the failing and warning checks by impact, then severity,
// then points recovered, then objects affected. n <= 0 returns all.
func Priorities(r Report, n int) []Priority {
	var out []Priority
	for _, c := range r.Checks {
		if c.Status != Fail && c.Status != Warn {
			continue
		}
		p := Priority{ID: c.ID, Area: c.Area, Title: c.Title, Severity: c.Severity, Status: c.Status,
			Points: PointsIfFixed(r, c), Impact: math.Round(Impact(c)*100) / 100, Total: c.Total, Fix: c.Fix, Source: c.Source, Docs: c.Docs, Objects: []string{}}
		for _, it := range c.Items {
			switch it.Status {
			case Fail:
				p.Failing++
			case Warn:
				p.Warning++
			default:
				continue
			}
			if len(p.Objects) < 5 {
				name := strings.Trim(it.Namespace+"/"+it.Name, "/")
				if name == "" {
					name = "cluster"
				}
				p.Objects = append(p.Objects, strings.TrimSpace(it.Kind+" "+name))
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Impact != b.Impact {
			return a.Impact > b.Impact
		}
		if weight[a.Severity] != weight[b.Severity] {
			return weight[a.Severity] > weight[b.Severity]
		}
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if statusRank[a.Status] != statusRank[b.Status] {
			return statusRank[a.Status] < statusRank[b.Status]
		}
		return a.Failing+a.Warning > b.Failing+b.Warning
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
