package reach

import (
	"fmt"
	"sort"
	"strings"
)

// An agentgateway MCP authorization rule is a CEL expression. Flightbay reads
// the part of CEL these rules are written in: || and && with brackets, !,
// true and false, field == "value" and field in ["a", "b"]. Anything else
// (a method call, a comparison on another field) is kept as text: a
// condition the capture cannot decide.

// ruleClause is one way through a rule: the callers it admits, the tools it
// covers (nil: every tool) and the conditions it could not decide.
type ruleClause struct {
	who   []string // "kagent/triage", "service account triage", "namespace kagent", "JWT subject alice", "any caller"
	tools []string // nil: every tool of the server
	conds []string
	// saNoNS lists service accounts it names without a namespace: any
	// namespace with a service account of that name matches.
	saNoNS []string
}

type celTok struct {
	kind       string // id, str, op, (, ), [, ], ",", other
	text       string
	start, end int
}

func celLex(src string) ([]celTok, error) {
	var out []celTok
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var b strings.Builder
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				b.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, celTok{"str", b.String(), i, j + 1})
			i = j + 1
		case strings.HasPrefix(src[i:], "||") || strings.HasPrefix(src[i:], "&&") || strings.HasPrefix(src[i:], "==") || strings.HasPrefix(src[i:], "!=") || strings.HasPrefix(src[i:], ">=") || strings.HasPrefix(src[i:], "<="):
			out = append(out, celTok{"op", src[i : i+2], i, i + 2})
			i += 2
		case strings.ContainsRune("()[],", rune(c)):
			out = append(out, celTok{string(c), string(c), i, i + 1})
			i++
		case c == '!' || c == '<' || c == '>' || c == '?' || c == ':' || c == '+' || c == '-' || c == '*' || c == '/' || c == '%':
			out = append(out, celTok{"op", string(c), i, i + 1})
			i++
		case c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '.' || (src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= '0' && src[j] <= '9')) {
				j++
			}
			out = append(out, celTok{"id", src[i:j], i, j})
			i = j
		default:
			out = append(out, celTok{"other", string(c), i, i + 1})
			i++
		}
	}
	return out, nil
}

// celNode is the expression as far as Flightbay reads it.
type celNode struct {
	op     string // or, and, not, true, false, atom, cond
	kids   []*celNode
	field  string   // atom: the field compared
	values []string // atom: the values it may equal
	raw    string   // cond: the expression as written
}

type celParser struct {
	src  string
	toks []celTok
	i    int
}

func (p *celParser) peek() *celTok {
	if p.i < len(p.toks) {
		return &p.toks[p.i]
	}
	return nil
}

func (p *celParser) is(kind, text string) bool {
	t := p.peek()
	return t != nil && t.kind == kind && (text == "" || t.text == text)
}

func (p *celParser) or() (*celNode, error) {
	n, err := p.and()
	if err != nil {
		return nil, err
	}
	kids := []*celNode{n}
	for p.is("op", "||") {
		p.i++
		k, err := p.and()
		if err != nil {
			return nil, err
		}
		kids = append(kids, k)
	}
	if len(kids) == 1 {
		return n, nil
	}
	return &celNode{op: "or", kids: kids}, nil
}

func (p *celParser) and() (*celNode, error) {
	n, err := p.unary()
	if err != nil {
		return nil, err
	}
	kids := []*celNode{n}
	for p.is("op", "&&") {
		p.i++
		k, err := p.unary()
		if err != nil {
			return nil, err
		}
		kids = append(kids, k)
	}
	if len(kids) == 1 {
		return n, nil
	}
	return &celNode{op: "and", kids: kids}, nil
}

func (p *celParser) unary() (*celNode, error) {
	if p.is("op", "!") {
		start := p.peek().start
		p.i++
		k, err := p.unary()
		if err != nil {
			return nil, err
		}
		switch k.op {
		case "true":
			return &celNode{op: "false"}, nil
		case "false":
			return &celNode{op: "true"}, nil
		}
		return &celNode{op: "cond", raw: p.text(start)}, nil
	}
	if p.is("(", "") {
		start := p.peek().start
		p.i++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.is(")", "") {
			return nil, fmt.Errorf("missing )")
		}
		p.i++
		if p.comparisonNext() {
			// (a) == b: not a shape these rules use; keep it as written.
			if err := p.skipOperand(); err != nil {
				return nil, err
			}
			return &celNode{op: "cond", raw: p.text(start)}, nil
		}
		return n, nil
	}
	return p.comparison()
}

func (p *celParser) comparisonNext() bool {
	t := p.peek()
	return t != nil && ((t.kind == "op" && t.text != "||" && t.text != "&&" && t.text != "!") || (t.kind == "id" && t.text == "in"))
}

// comparison reads operand [op operand].
func (p *celParser) comparison() (*celNode, error) {
	t := p.peek()
	if t == nil {
		return nil, fmt.Errorf("expression ends early")
	}
	start := t.start
	if t.kind == "id" && (t.text == "true" || t.text == "false") && !p.callNext(1) {
		p.i++
		if !p.comparisonNext() {
			return &celNode{op: t.text}, nil
		}
		p.i-- // true == x: read it as an operand
	}
	lhs, lField, lStr, err := p.operand()
	if err != nil {
		return nil, err
	}
	if !p.comparisonNext() {
		return &celNode{op: "cond", raw: p.text(start)}, nil
	}
	op := p.peek().text
	p.i++
	rhs, rField, rStr, err := p.operand()
	if err != nil {
		return nil, err
	}
	raw := p.text(start)
	switch {
	case op == "==" && lField != "" && rStr:
		return &celNode{op: "atom", field: lField, values: rhs}, nil
	case op == "==" && rField != "" && lStr:
		return &celNode{op: "atom", field: rField, values: lhs}, nil
	case op == "in" && lField != "" && rhs != nil && !rStr && rField == "":
		return &celNode{op: "atom", field: lField, values: rhs}, nil
	}
	return &celNode{op: "cond", raw: raw}, nil
}

func (p *celParser) callNext(off int) bool {
	return p.i+off < len(p.toks) && p.toks[p.i+off].kind == "("
}

// operand reads a field, a string, a list of strings or anything else up to
// the next operator. It returns the strings (a list or one string), the field
// name when it is a plain field, and whether it was a single string.
func (p *celParser) operand() (vals []string, field string, isStr bool, err error) {
	t := p.peek()
	if t == nil {
		return nil, "", false, fmt.Errorf("expression ends early")
	}
	switch {
	case t.kind == "str":
		p.i++
		return []string{t.text}, "", true, nil
	case t.kind == "[":
		p.i++
		var out []string
		allStr := true
		for !p.is("]", "") {
			x := p.peek()
			if x == nil {
				return nil, "", false, fmt.Errorf("missing ]")
			}
			if x.kind == "str" {
				out = append(out, x.text)
			} else if x.kind != "," {
				allStr = false
			}
			p.i++
		}
		p.i++
		if !allStr {
			return nil, "", false, nil
		}
		if out == nil {
			out = []string{}
		}
		return out, "", false, nil
	case t.kind == "id" && !p.callNext(1):
		p.i++
		// a.b[0] or a.b["k"]: an index is not a plain field.
		if p.is("[", "") {
			if err := p.skipBalanced(); err != nil {
				return nil, "", false, err
			}
			return nil, "", false, nil
		}
		return nil, t.text, false, nil
	}
	return nil, "", false, p.skipOperand()
}

// skipOperand steps over one operand this reader does not interpret: a call
// with its arguments, an index, a number.
func (p *celParser) skipOperand() error {
	for {
		t := p.peek()
		if t == nil || t.kind == ")" || t.kind == "]" || t.kind == "," || p.comparisonNext() || (t.kind == "op" && (t.text == "||" || t.text == "&&")) {
			return nil
		}
		if t.kind == "(" || t.kind == "[" {
			if err := p.skipBalanced(); err != nil {
				return err
			}
			continue
		}
		p.i++
	}
}

func (p *celParser) skipBalanced() error {
	depth := 0
	for t := p.peek(); t != nil; t = p.peek() {
		switch t.kind {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		}
		p.i++
		if depth == 0 {
			return nil
		}
	}
	return fmt.Errorf("unbalanced brackets")
}

func (p *celParser) text(start int) string {
	end := start
	if p.i > 0 && p.i <= len(p.toks) {
		end = p.toks[p.i-1].end
	}
	return strings.Join(strings.Fields(p.src[start:end]), " ")
}

// maxClauses bounds the expansion of a rule into its ways through; a rule
// that expands further is reported as not read.
const maxClauses = 512

// dnf expands n into its ways through: each a list of atoms and conditions
// that must all hold.
func dnf(n *celNode) ([][]*celNode, error) {
	switch n.op {
	case "true":
		return [][]*celNode{{}}, nil
	case "false":
		return nil, nil
	case "atom", "cond":
		return [][]*celNode{{n}}, nil
	case "or":
		var out [][]*celNode
		for _, k := range n.kids {
			d, err := dnf(k)
			if err != nil {
				return nil, err
			}
			out = append(out, d...)
			if len(out) > maxClauses {
				return nil, fmt.Errorf("too many ways through")
			}
		}
		return out, nil
	case "and":
		out := [][]*celNode{{}}
		for _, k := range n.kids {
			d, err := dnf(k)
			if err != nil {
				return nil, err
			}
			var next [][]*celNode
			for _, a := range out {
				for _, b := range d {
					c := append(append([]*celNode{}, a...), b...)
					next = append(next, c)
				}
			}
			if len(next) > maxClauses {
				return nil, fmt.Errorf("too many ways through")
			}
			out = next
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown node %q", n.op)
}

// readMCPRule reads one matchExpressions entry into its ways through. ok is
// false when the expression is not one this reader understands at all.
func readMCPRule(expr string) (clauses []ruleClause, ok bool) {
	toks, err := celLex(expr)
	if err != nil || len(toks) == 0 {
		return nil, false
	}
	p := &celParser{src: expr, toks: toks}
	n, err := p.or()
	if err != nil || p.i != len(toks) {
		return nil, false
	}
	ways, err := dnf(n)
	if err != nil {
		return nil, false
	}
	for _, w := range ways {
		if c, possible := clauseOf(w); possible {
			clauses = append(clauses, c)
		}
	}
	return clauses, true
}

// clauseOf reads one way through. possible is false when it can never hold
// (a field required to equal two different values).
func clauseOf(atoms []*celNode) (c ruleClause, possible bool) {
	sets := map[string][]string{}
	has := map[string]bool{}
	for _, a := range atoms {
		if a.op == "cond" {
			c.conds = append(c.conds, a.raw)
			continue
		}
		if !has[a.field] {
			has[a.field], sets[a.field] = true, append([]string{}, a.values...)
			continue
		}
		sets[a.field] = intersect(sets[a.field], a.values)
	}
	for f := range has {
		if len(sets[f]) == 0 {
			return c, false
		}
	}
	// Who a clause admits: a mesh identity if it names one, else a JWT
	// subject, else the first JWT claim it tests. Everything else it tests
	// is a condition.
	mesh := has["source.identity.serviceAccount"] || has["source.identity.namespace"]
	claim := ""
	if !mesh && !has["jwt.sub"] {
		for _, f := range sortedKeys(has) {
			if strings.HasPrefix(f, "jwt.") {
				claim = f
				break
			}
		}
	}
	for _, f := range sortedKeys(has) {
		switch {
		case f == "mcp.tool.name", f == "source.identity.serviceAccount", f == "source.identity.namespace":
		case f == "jwt.sub" && !mesh:
		case f == claim:
		default:
			c.conds = append(c.conds, condText(f, sets[f]))
		}
	}
	if has["mcp.tool.name"] {
		c.tools = sets["mcp.tool.name"]
	}
	sas, nss := sets["source.identity.serviceAccount"], sets["source.identity.namespace"]
	switch {
	case len(sas) > 0 && len(nss) > 0:
		for _, ns := range nss {
			for _, sa := range sas {
				c.who = append(c.who, ns+"/"+sa)
			}
		}
	case len(sas) > 0:
		for _, sa := range sas {
			c.who = append(c.who, "service account "+sa)
		}
		c.saNoNS = sas
	case len(nss) > 0:
		for _, ns := range nss {
			c.who = append(c.who, "namespace "+ns)
		}
	case has["jwt.sub"]:
		for _, u := range sets["jwt.sub"] {
			c.who = append(c.who, "JWT subject "+u)
		}
	case claim != "":
		for _, v := range sets[claim] {
			c.who = append(c.who, "JWT "+strings.TrimPrefix(claim, "jwt.")+" "+v)
		}
	}
	if len(c.who) == 0 {
		c.who = []string{"any caller"}
	}
	if len(c.conds) > 0 {
		sort.Strings(c.conds)
		suffix := " (if " + strings.Join(c.conds, " && ") + ")"
		for i := range c.who {
			c.who[i] += suffix
		}
	}
	return c, true
}

func condText(field string, vals []string) string {
	if len(vals) == 1 {
		return fmt.Sprintf("%s == %q", field, vals[0])
	}
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = fmt.Sprintf("%q", v)
	}
	return fmt.Sprintf("%s in [%s]", field, strings.Join(q, ", "))
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}
