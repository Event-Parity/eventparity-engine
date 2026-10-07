package model

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// StreamFormat identifies the JSON Lines stream layout produced by adapters and accepted as a candidate.
const StreamFormat = "eventparity-stream/v1"

// Scope is the only observation scope in version 1.
const Scope = "classic-payments"

// Asset is issuer-aware: a credit asset is only identified by code and issuer together.
type Asset struct {
	Type   string `json:"type"` // "native" or "credit"
	Code   string `json:"code,omitempty"`
	Issuer string `json:"issuer,omitempty"`
}

func (a Asset) String() string {
	if a.Type == "native" {
		return "native"
	}
	return a.Code + ":" + a.Issuer
}

// Payment is one successful classic `payment` operation, identified by transaction hash and operation index.
type Payment struct {
	Ledger           uint32 `json:"ledger"`
	ApplicationOrder uint32 `json:"applicationOrder"`
	TxHash           string `json:"txHash"`
	OpIndex          uint32 `json:"opIndex"`
	From             string `json:"from"`
	FromMuxedID      string `json:"fromMuxedId,omitempty"`
	To               string `json:"to"`
	ToMuxedID        string `json:"toMuxedId,omitempty"`
	Asset            Asset  `json:"asset"`
	Amount           string `json:"amount"` // canonical 7-decimal string
}

// Key is the operation identity used to pair observations across sources.
func (p Payment) Key() string { return p.TxHash + ":" + strconv.FormatUint(uint64(p.OpIndex), 10) }

var (
	hashRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	addrRe  = regexp.MustCompile(`^G[A-Z2-7]{55}$`)
	codeRe  = regexp.MustCompile(`^[a-zA-Z0-9]{1,12}$`)
	muxIDRe = regexp.MustCompile(`^[0-9]{1,20}$`)
)

func validAccount(a string) bool { return strkey.IsValidEd25519PublicKey(a) }

// Validate rejects malformed observations before they can reach the comparator.
func (p Payment) Validate() error {
	if !hashRe.MatchString(p.TxHash) {
		return fmt.Errorf("txHash %q is not 64 lowercase hex characters", p.TxHash)
	}
	if !validAccount(p.From) || !validAccount(p.To) {
		return fmt.Errorf("from/to must be valid G... account addresses (muxed ids go in fromMuxedId/toMuxedId)")
	}
	if p.FromMuxedID != "" && !muxIDRe.MatchString(p.FromMuxedID) || p.ToMuxedID != "" && !muxIDRe.MatchString(p.ToMuxedID) {
		return fmt.Errorf("muxed ids must be unsigned decimal integers")
	}
	switch p.Asset.Type {
	case "native":
		if p.Asset.Code != "" || p.Asset.Issuer != "" {
			return fmt.Errorf("native asset must not carry code or issuer")
		}
	case "credit":
		if !codeRe.MatchString(p.Asset.Code) || !validAccount(p.Asset.Issuer) {
			return fmt.Errorf("credit asset needs a 1-12 character code and a G... issuer")
		}
	default:
		return fmt.Errorf("asset type %q must be native or credit", p.Asset.Type)
	}
	if c, err := CanonicalAmount(p.Amount); err != nil {
		return err
	} else if c != p.Amount {
		return fmt.Errorf("amount %q is not canonical (want %q)", p.Amount, c)
	}
	if p.Ledger == 0 {
		return fmt.Errorf("ledger must be set")
	}
	return nil
}

// Range is an inclusive ledger range.
type Range struct {
	From uint32 `json:"from"`
	To   uint32 `json:"to"`
}

func (r Range) Contains(l uint32) bool { return l >= r.From && l <= r.To }
func (r Range) Len() uint32            { return r.To - r.From + 1 }

// Gap is a requested range a source could not cover, with the reason. A gap is never evidence of missing payments.
type Gap struct {
	Range
	Reason string `json:"reason"`
}

// Header is the first line of a stream.
type Header struct {
	Type      string `json:"type"` // "header"
	Format    string `json:"format"`
	Adapter   string `json:"adapter"`  // horizon, rpc or candidate
	Provider  string `json:"provider"` // origin only
	Network   string `json:"network,omitempty"`
	Requested Range  `json:"requested"`
	CreatedAt string `json:"createdAt"`
	Scope     string `json:"scope"`
	Tool      string `json:"tool,omitempty"`
}

// LedgerNote records, for a ledger, successful operations this version does not compare (by Horizon type name).
type LedgerNote struct {
	Type        string         `json:"type"` // "ledger"
	Ledger      uint32         `json:"ledger"`
	Unsupported map[string]int `json:"unsupported"`
}

// Coverage is the last line of a complete stream: what was actually read, and what was not.
type Coverage struct {
	Type     string  `json:"type"` // "coverage"
	Covered  []Range `json:"covered"`
	Gaps     []Gap   `json:"gaps"`
	Complete bool    `json:"complete"` // true when covered equals the requested range with no gaps
}

// Stream is a fully read stream.
type Stream struct {
	Header      Header
	Payments    []Payment
	Unsupported map[string]int // total by operation type across the stream
	Coverage    Coverage
}

// SortedTypes returns the unsupported operation type names in a stable order.
func SortedTypes(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
