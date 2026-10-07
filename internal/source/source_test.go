package source

import "testing"

func TestOpTypeNameMatchesHorizonNames(t *testing.T) {
	cases := map[string]string{
		"OperationTypePayment":                       "payment",
		"OperationTypeManageSellOffer":               "manage_sell_offer",
		"OperationTypePathPaymentStrictSend":         "path_payment_strict_send",
		"OperationTypeSetTrustLineFlags":             "set_trust_line_flags",
		"OperationTypeInvokeHostFunction":            "invoke_host_function",
		"OperationTypeExtendFootprintTtl":            "extend_footprint_ttl",
		"OperationTypeBeginSponsoringFutureReserves": "begin_sponsoring_future_reserves",
		"OperationTypeLiquidityPoolDeposit":          "liquidity_pool_deposit",
	}
	for in, want := range cases {
		if got := OpTypeName(in); got != want {
			t.Errorf("OpTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
