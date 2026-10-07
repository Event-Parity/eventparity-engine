// Package source defines what an ingestion adapter must provide and the shared operation-type naming.
package source

import (
	"context"
	"regexp"
	"strings"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
)

// Bounds is the ledger window a provider can currently serve.
type Bounds struct {
	Oldest  uint32
	Latest  uint32
	Network string // network passphrase when the provider reports it
}

// LedgerData is everything an adapter observed in one ledger: supported payments and counts of unsupported operations.
type LedgerData struct {
	Ledger      uint32
	Payments    []model.Payment
	Unsupported map[string]int
}

// Source is an ingestion adapter. Stream must emit every ledger in [from, to] exactly once, in ascending order,
// including ledgers with no operations, so that coverage is exact. It must return an error rather than skip a ledger.
type Source interface {
	Adapter() string  // "horizon" or "rpc"
	Provider() string // origin only
	Bounds(ctx context.Context) (Bounds, error)
	Stream(ctx context.Context, from, to uint32, emit func(LedgerData) error) error
}

var camel = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// OpTypeName converts the Stellar Go SDK's "OperationTypeManageSellOffer" to Horizon's "manage_sell_offer".
func OpTypeName(sdkName string) string {
	n := strings.TrimPrefix(sdkName, "OperationType")
	return strings.ToLower(camel.ReplaceAllString(n, "${1}_${2}"))
}
