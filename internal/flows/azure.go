package flows

import (
	"encoding/json"
	"strings"
)

// ParseAzure converts an Azure flow log blob: NSG flow logs (version 1 and 2,
// records[].properties.flows[].flows[].flowTuples) or VNet flow logs
// (records[].flowRecords.flows[].flowGroups[].flowTuples). A blob is one JSON
// document, so this takes the whole file.
//
// NSG v2 tuple:  ts,srcIP,dstIP,srcPort,dstPort,proto(T|U),dir(I|O),decision(A|D),state(B|C|E),pkts,bytes,pkts,bytes
// VNet tuple:    ts,srcIP,dstIP,srcPort,dstPort,proto(6|17),dir(I|O),state(B|C|E|D),encryption,pkts,bytes,pkts,bytes
//
// Only tuples that begin a flow (state B), or deny it, are counted; C and E
// are updates to a flow already counted.
func ParseAzure(doc []byte) ([]Flow, error) {
	var root struct {
		Records []struct {
			Properties struct {
				Version int `json:"Version"`
				Flows   []struct {
					Rule  string `json:"rule"`
					Flows []struct {
						FlowTuples []string `json:"flowTuples"`
					} `json:"flows"`
				} `json:"flows"`
			} `json:"properties"`
			FlowRecords struct {
				Flows []struct {
					FlowGroups []struct {
						Rule       string   `json:"rule"`
						FlowTuples []string `json:"flowTuples"`
					} `json:"flowGroups"`
				} `json:"flows"`
			} `json:"flowRecords"`
		} `json:"records"`
	}
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	var out []Flow
	for _, r := range root.Records {
		for _, g := range r.Properties.Flows {
			for _, ff := range g.Flows {
				for _, t := range ff.FlowTuples {
					if f, ok := azureTuple(t, false, g.Rule); ok {
						out = append(out, f)
					}
				}
			}
		}
		for _, fl := range r.FlowRecords.Flows {
			for _, g := range fl.FlowGroups {
				for _, t := range g.FlowTuples {
					if f, ok := azureTuple(t, true, g.Rule); ok {
						out = append(out, f)
					}
				}
			}
		}
	}
	return out, nil
}

func azureTuple(t string, vnet bool, rule string) (Flow, bool) {
	p := strings.Split(t, ",")
	if len(p) < 8 {
		return Flow{}, false
	}
	f := Flow{Source: FmtAzure, Count: 1, SrcIP: p[1], DstIP: p[2], DstPort: atoi(p[4]), Protocol: ProtoName(p[5]), Route: rule}
	if !isIP(f.SrcIP) || !isIP(f.DstIP) {
		return Flow{}, false
	}
	if vnet {
		switch p[7] {
		case "B":
			f.Verdict = Allowed
		case "D":
			f.Verdict = Denied
		default:
			return Flow{}, false
		}
		return f, true
	}
	f.Verdict = verdictOf(p[7])
	if len(p) > 8 && p[8] != "" && p[8] != "B" && f.Verdict != Denied {
		return Flow{}, false // v2 continuation or end of a flow already counted
	}
	return f, true
}
