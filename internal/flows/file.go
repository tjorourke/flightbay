package flows

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// MaxLine is the longest single record a line-oriented flow file may hold.
const MaxLine = 16 << 20

func firstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// Detect names the format of a flow file from its first bytes: one of
// Formats, or "" when it is not recognised.
func Detect(head []byte) string {
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	t := bytes.TrimSpace(head)
	if len(t) == 0 {
		return ""
	}
	has := func(s string) bool { return bytes.Contains(t, []byte(s)) }
	if t[0] == '{' || t[0] == '[' {
		switch {
		case has(`"flowTuples"`):
			return FmtAzure
		case has(`"jsonPayload"`) && has(`"connection"`), has(`"connection"`) && has(`"src_ip"`) && has(`"dest_ip"`):
			return FmtGCP
		case has(`"source_name_aggr"`), has(`"dest_name_aggr"`), has(`"num_flows"`) && has(`"reporter"`):
			return FmtCalico
		case has(`"flow"`) && (has(`"verdict"`) || has(`"IP"`)), has(`"verdict"`) && (has(`"l4"`) || has(`"IP"`)):
			return FmtHubble
		}
		l := firstLine(t)
		if l != "" && l[0] == '[' {
			l = strings.TrimSpace(strings.TrimPrefix(l, "["))
		}
		if _, ok := ParseGenericJSON([]byte(strings.TrimSuffix(l, ","))); ok {
			return FmtGeneric
		}
		if has(`"src_ip"`) || has(`"srcIp"`) || has(`"dst_host"`) || has(`"dest_ip"`) {
			return FmtGeneric
		}
		return ""
	}
	l := firstLine(t)
	if awsHeader(l) != nil {
		return FmtAWS
	}
	if f := strings.Fields(l); len(f) == len(awsDefaultFields) && atoi(f[0]) > 0 && (strings.HasPrefix(f[2], "eni-") || f[2] == "-") {
		return FmtAWS
	}
	if genericHeader(l, ',') || genericHeader(l, '\t') || genericHeader(l, ';') {
		return FmtGeneric
	}
	return ""
}

// ParseFile reads a flow file in the given format ("" to detect it) and
// returns its flows, aggregated, with the detected format.
func ParseFile(r io.Reader, format string) ([]Flow, string, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	head, _ := br.Peek(64 << 10)
	if format == "" {
		format = Detect(head)
		if format == "" {
			return nil, "", fmt.Errorf("unrecognised flow log format (expected one of %s)", strings.Join(Formats, ", "))
		}
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")))
	var out []Flow
	lines := func(fn func([]byte) (Flow, bool)) error {
		if len(trimmed) > 0 && trimmed[0] == '[' {
			// A JSON array export (Cloud Logging, SIEM): one record per element.
			dec := json.NewDecoder(br)
			var arr []json.RawMessage
			if err := dec.Decode(&arr); err != nil {
				return err
			}
			for _, e := range arr {
				if f, ok := fn(e); ok {
					out = append(out, f)
				}
			}
			return nil
		}
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 1<<20), MaxLine)
		for sc.Scan() {
			l := bytes.TrimSpace(sc.Bytes())
			if len(l) == 0 {
				continue
			}
			if f, ok := fn(l); ok {
				out = append(out, f)
			}
		}
		return sc.Err()
	}
	var err error
	switch format {
	case FmtHubble:
		err = lines(ParseHubble)
	case FmtCalico:
		err = lines(ParseCalico)
	case FmtGCP:
		err = lines(ParseGCP)
	case FmtAWS:
		p := &AWSParser{}
		err = lines(func(b []byte) (Flow, bool) { return p.Line(string(b)) })
	case FmtAzure:
		var doc []byte
		if doc, err = io.ReadAll(br); err == nil {
			out, err = ParseAzure(doc)
		}
	case FmtGeneric:
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			err = lines(ParseGenericJSON)
		} else {
			l := firstLine(trimmed)
			comma := ','
			switch {
			case genericHeader(l, '\t'):
				comma = '\t'
			case genericHeader(l, ';') && !genericHeader(l, ','):
				comma = ';'
			}
			out, err = ParseGenericCSV(br, comma)
		}
	default:
		return nil, format, fmt.Errorf("unknown flow log format %q (use one of %s)", format, strings.Join(Formats, ", "))
	}
	if err != nil {
		return Aggregate(out), format, err
	}
	return Aggregate(out), format, nil
}

// Namespaces lists the Kubernetes namespaces a flow record names, for policy
// filtering: any JSON value under a key ending in "namespace" (hubble,
// Calico, GCP GKE details, generic), the pod-namespace label hubble carries,
// or a CSV column named like one.
func Namespaces(record []byte) []string {
	var v any
	if json.Unmarshal(record, &v) != nil {
		return nil
	}
	var out []string
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				walk(k, x)
			}
		case []any:
			for _, x := range t {
				walk(key, x)
			}
		case string:
			if strings.HasSuffix(normCol(key), "namespace") && t != "" && t != "-" {
				out = append(out, t)
			}
			if i := strings.Index(t, "io.kubernetes.pod.namespace="); i >= 0 {
				out = append(out, t[i+len("io.kubernetes.pod.namespace="):])
			}
		}
	}
	walk("", v)
	return out
}

// Filter rewrites a flow file for a bundle: records naming a namespace that
// allowed rejects are dropped, and every kept line goes through scrub (the
// collector's redactor). JSON array exports become JSON Lines so each record
// can be judged on its own. Formats with no namespaces (AWS, Azure) are only
// scrubbed. It returns the new content and how many records were dropped.
func Filter(data []byte, format string, allowed func(ns string) bool, scrub func(string) string) ([]byte, int, error) {
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	t := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	var out bytes.Buffer
	dropped := 0
	keep := func(rec []byte) bool {
		for _, ns := range Namespaces(rec) {
			if !allowed(ns) {
				return false
			}
		}
		return true
	}
	switch {
	case format == FmtAzure || format == FmtAWS:
		for _, l := range strings.SplitAfter(string(data), "\n") {
			out.WriteString(scrub(l))
		}
		return out.Bytes(), 0, nil
	case len(t) > 0 && t[0] == '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(t, &arr); err != nil {
			return nil, 0, err
		}
		for _, e := range arr {
			var c bytes.Buffer
			if json.Compact(&c, e) != nil {
				continue
			}
			if !keep(c.Bytes()) {
				dropped++
				continue
			}
			out.WriteString(scrub(c.String()))
			out.WriteByte('\n')
		}
		return out.Bytes(), dropped, nil
	case len(t) > 0 && t[0] == '{':
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if !keep([]byte(l)) {
				dropped++
				continue
			}
			out.WriteString(scrub(l))
			out.WriteByte('\n')
		}
		return out.Bytes(), dropped, nil
	}
	// CSV / TSV: find the namespace columns from the header.
	ls := strings.Split(string(data), "\n")
	var nsCols []int
	comma := ","
	for i, l := range ls {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if strings.Count(l, "\t") > strings.Count(l, ",") {
			comma = "\t"
		}
		for j, h := range strings.Split(l, comma) {
			if strings.HasSuffix(normCol(strings.Trim(h, "\" \r")), "namespace") {
				nsCols = append(nsCols, j)
			}
		}
		out.WriteString(scrub(l))
		out.WriteByte('\n')
		ls = ls[i+1:]
		break
	}
	for _, l := range ls {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cells := strings.Split(l, comma)
		ok := true
		for _, j := range nsCols {
			if j < len(cells) {
				ns := strings.Trim(cells[j], "\" \r")
				if ns != "" && ns != "-" && !allowed(ns) {
					ok = false
				}
			}
		}
		if !ok {
			dropped++
			continue
		}
		out.WriteString(scrub(l))
		out.WriteByte('\n')
	}
	return out.Bytes(), dropped, nil
}
