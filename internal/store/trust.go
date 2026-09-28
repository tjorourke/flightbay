package store

import (
	"encoding/json"

	"github.com/tjorourke/flightbay/internal/sign"
)

// Trust is what the desk knows about where a bundle came from: its
// signature, checked once the whole bundle has been read, and the ledger of
// what the customer removed in review.
type Trust struct {
	Signature *sign.Result    `json:"signature,omitempty"`
	Ledger    json.RawMessage `json:"ledger,omitempty"`
}

// SetSignature records the result of checking manifest.sig.
func (w *Workspace) SetSignature(r sign.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trust.Signature = &r
	w.commit()
}

// SetLedger records review-ledger.json.
func (w *Workspace) SetLedger(raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trust.Ledger = append(json.RawMessage{}, raw...)
	w.commit()
}

// Trust returns the signature and ledger recorded so far.
func (w *Workspace) Trust() Trust {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.trust
}
