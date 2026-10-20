package diff

import (
	"fmt"
	"strings"
)

// Unified returns a line-wise unified diff of a and b with ctx lines of
// context, labelled fromName and toName. Equal inputs give "".
func Unified(a, b, fromName, toName string, ctx int) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	ops := lineOps(al, bl)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", fromName, toName)
	// Group the edit script into hunks separated by more than 2*ctx equal lines.
	i := 0
	for i < len(ops) {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i >= len(ops) {
			break
		}
		start := max(i-ctx, 0) // never reaches back into the previous hunk: that ended ctx lines after its last change
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run >= len(ops) || run-end > 2*ctx {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		h := ops[start:end]
		aStart, bStart := h[0].ai, h[0].bi
		aN, bN := 0, 0
		for _, o := range h {
			if o.kind != '+' {
				aN++
			}
			if o.kind != '-' {
				bN++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(aStart, aN), hunkRange(bStart, bN))
		for _, o := range h {
			sb.WriteByte(o.kind)
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type lineOp struct {
	kind   byte // ' ', '-' or '+'
	text   string
	ai, bi int // index of this line (or the insertion point) in a and b
}

// lineOps is a longest-common-subsequence edit script. Object YAML is a few
// hundred lines at most, so the quadratic table is fine.
func lineOps(a, b []string) []lineOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []lineOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i], i, j})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] > lcs[i+1][j]):
			ops = append(ops, lineOp{'+', b[j], i, j})
			j++
		default:
			ops = append(ops, lineOp{'-', a[i], i, j})
			i++
		}
	}
	return ops
}
