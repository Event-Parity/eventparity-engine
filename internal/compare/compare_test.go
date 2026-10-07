package compare

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
)

// Real testnet addresses from the recorded corpus ground truth (the model validates checksums).
const (
	addrA = "GAUGPIMKP4ZQ22FEUGDSTBJWTGIS5LDB5ZYUSQI33G37DFWLTY34OFAZ"
	addrB = "GBYFGKRQVWELTTR3425EQXZP4US5KSHAMJUQNH42AYUI54IJN6M4RQB4"
	addrI = "GDM3YHMRTRHLXI4MHQSMT4N42TAGPFCMSMZPO7UHJ4I7ZHZ5MVWXGPK2"
)

func hash(n int) string { return strings.Repeat(string(rune('a'+n%6)), 63) + string(rune('0'+n%10)) }

func pay(ledger uint32, order uint32, n int, op uint32, amount string) model.Payment {
	return model.Payment{Ledger: ledger, ApplicationOrder: order, TxHash: hash(n), OpIndex: op, From: addrA, To: addrB,
		Asset: model.Asset{Type: "native"}, Amount: amount}
}

func stream(from, to uint32, covered []model.Range, gaps []model.Gap, ps ...model.Payment) *model.Stream {
	return &model.Stream{
		Header:              model.Header{Requested: model.Range{From: from, To: to}, Adapter: "test", Provider: "t"},
		Payments:            ps,
		Unsupported:         map[string]int{},
		UnsupportedByLedger: map[uint32]map[string]int{},
		Coverage:            model.Coverage{Covered: covered, Gaps: gaps, Complete: len(gaps) == 0},
	}
}

func full(from, to uint32, ps ...model.Payment) *model.Stream {
	return stream(from, to, []model.Range{{From: from, To: to}}, nil, ps...)
}

func run(t *testing.T, ref, cand *model.Stream) *Result {
	t.Helper()
	r, err := Compare(Input{Reference: ref, Candidate: cand, ReferenceName: "ref", CandidateName: "cand"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func classes(r *Result) []Class {
	var c []Class
	for _, d := range r.Differences {
		c = append(c, d.Class)
	}
	return c
}

func TestParityWhenIdenticalAndFullyCovered(t *testing.T) {
	a, b := pay(10, 1, 1, 0, "1.0000000"), pay(11, 2, 2, 0, "2.0000000")
	r := run(t, full(10, 12, a, b), full(10, 12, b, a)) // order of lines does not matter
	if r.Verdict != Parity || r.Matched != 2 || len(r.Differences) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestDroppedAndDuplicatedPayments(t *testing.T) {
	a, b, c := pay(10, 1, 1, 0, "1.0000000"), pay(11, 1, 2, 0, "2.0000000"), pay(12, 1, 3, 0, "3.0000000")
	r := run(t, full(10, 12, a, b, c), full(10, 12, a, c, c)) // b dropped, c duplicated
	got := classes(r)
	if r.Verdict != Differences || len(got) != 2 || got[0] != MissingInCandidate || got[1] != DuplicatedInCandidate {
		t.Fatalf("verdict %s classes %v", r.Verdict, got)
	}
	if r.Differences[0].Key != b.Key() || r.Differences[1].Key != c.Key() || len(r.Differences[1].Candidate) != 2 {
		t.Fatalf("wrong evidence: %+v", r.Differences)
	}
	if r.Matched != 2 { // a and c both exist on both sides
		t.Fatalf("matched = %d", r.Matched)
	}
}

func TestReinterpretedNamesEveryDifferingField(t *testing.T) {
	base := pay(10, 1, 1, 0, "1.0000000")
	mutations := map[string]func(p *model.Payment){
		"amount":           func(p *model.Payment) { p.Amount = "1.0000001" },
		"to":               func(p *model.Payment) { p.To = addrI },
		"from":             func(p *model.Payment) { p.From = addrI },
		"asset":            func(p *model.Payment) { p.Asset = model.Asset{Type: "credit", Code: "USDX", Issuer: addrI} },
		"toMuxedId":        func(p *model.Payment) { p.ToMuxedID = "42" },
		"fromMuxedId":      func(p *model.Payment) { p.FromMuxedID = "7" },
		"ledger":           func(p *model.Payment) { p.Ledger = 11 },
		"applicationOrder": func(p *model.Payment) { p.ApplicationOrder = 9 },
	}
	for field, mutate := range mutations {
		cand := base
		mutate(&cand)
		r := run(t, full(10, 12, base), full(10, 12, cand))
		if len(r.Differences) != 1 || r.Differences[0].Class != Reinterpreted || len(r.Differences[0].Fields) != 1 || r.Differences[0].Fields[0] != field {
			t.Errorf("%s: %+v", field, r.Differences)
		}
		if r.Matched != 0 || r.Verdict != Differences {
			t.Errorf("%s: matched %d verdict %s", field, r.Matched, r.Verdict)
		}
	}
}

func TestSameCodeDifferentIssuerIsNotTheSameAsset(t *testing.T) {
	a := pay(10, 1, 1, 0, "1.0000000")
	a.Asset = model.Asset{Type: "credit", Code: "USDX", Issuer: addrA}
	b := a
	b.Asset.Issuer = addrB
	r := run(t, full(10, 12, a), full(10, 12, b))
	if len(r.Differences) != 1 || r.Differences[0].Fields[0] != "asset" {
		t.Fatalf("%+v", r.Differences)
	}
}

func TestUncoveredCandidateLedgerIsAGapNotAMissingPayment(t *testing.T) {
	a, b := pay(10, 1, 1, 0, "1.0000000"), pay(12, 1, 2, 0, "2.0000000")
	cand := stream(10, 12, []model.Range{{From: 10, To: 11}}, []model.Gap{{Range: model.Range{From: 12, To: 12}, Reason: "after provider's latest ledger"}}, a)
	r := run(t, full(10, 12, a, b), cand)
	if len(r.Differences) != 0 || r.NotComparedInGaps != 1 || r.Verdict != Inconclusive || r.Matched != 1 {
		t.Fatalf("verdict %s diffs %v notCompared %d", r.Verdict, r.Differences, r.NotComparedInGaps)
	}
	if len(r.Compared) != 1 || r.Compared[0] != (model.Range{From: 10, To: 11}) {
		t.Fatalf("compared %v", r.Compared)
	}
}

func TestUncoveredReferenceLedgerIsAlsoAGap(t *testing.T) {
	a := pay(10, 1, 1, 0, "1.0000000")
	ref := stream(10, 12, []model.Range{{From: 11, To: 12}}, []model.Gap{{Range: model.Range{From: 10, To: 10}, Reason: "before provider history"}})
	r := run(t, ref, full(10, 12, a))
	if len(r.Differences) != 0 || r.NotComparedInGaps != 1 || r.Verdict != Inconclusive {
		t.Fatalf("%+v", r)
	}
}

func TestDifferencesInCoveredLedgersStillReportedWhenGapsExist(t *testing.T) {
	a, b := pay(10, 1, 1, 0, "1.0000000"), pay(12, 1, 2, 0, "2.0000000")
	cand := stream(10, 12, []model.Range{{From: 10, To: 11}}, []model.Gap{{Range: model.Range{From: 12, To: 12}, Reason: "gap"}})
	r := run(t, full(10, 12, a, b), cand) // a missing from candidate in a covered ledger; b in the gap
	if r.Verdict != Differences || len(r.Differences) != 1 || r.Differences[0].Key != a.Key() || r.NotComparedInGaps != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestNoAllClearWithoutFullCoverage(t *testing.T) {
	ref := stream(10, 12, nil, []model.Gap{{Range: model.Range{From: 10, To: 12}, Reason: "outside retention"}})
	cand := full(10, 12)
	r := run(t, ref, cand)
	if r.Verdict == Parity {
		t.Fatal("an empty comparison must never be parity")
	}
	if len(r.Compared) != 0 {
		t.Fatalf("compared %v", r.Compared)
	}
}

func TestMissingInReferenceAndDuplicatedInReference(t *testing.T) {
	a, b := pay(10, 1, 1, 0, "1.0000000"), pay(11, 1, 2, 0, "2.0000000")
	r := run(t, full(10, 12, a, a), full(10, 12, a, b))
	got := classes(r)
	if len(got) != 2 || got[0] != DuplicatedInReference || got[1] != MissingInReference {
		t.Fatalf("%v", got)
	}
}

func TestUnsupportedCountsAreDisclosedButDoNotChangeTheVerdict(t *testing.T) {
	ref, cand := full(10, 12), full(10, 12)
	ref.UnsupportedByLedger[10] = map[string]int{"manage_data": 3, "invoke_host_function": 5}
	cand.UnsupportedByLedger[10] = map[string]int{"manage_data": 3, "invoke_host_function": 4}
	ref.UnsupportedByLedger[99] = map[string]int{"manage_data": 100} // outside the requested range: ignored
	r := run(t, ref, cand)
	if r.Verdict != Parity || len(r.Unsupported) != 1 || r.Unsupported[0] != (UnsupportedDiff{OpType: "invoke_host_function", Reference: 5, Candidate: 4}) {
		t.Fatalf("%+v", r)
	}
}

func TestUnsupportedInGapLedgersIsNotCounted(t *testing.T) {
	ref := full(10, 12)
	cand := stream(10, 12, []model.Range{{From: 10, To: 11}}, []model.Gap{{Range: model.Range{From: 12, To: 12}, Reason: "gap"}})
	ref.UnsupportedByLedger[12] = map[string]int{"manage_data": 9}
	r := run(t, ref, cand)
	if len(r.Unsupported) != 0 {
		t.Fatalf("%+v", r.Unsupported)
	}
}

func TestRefusesDifferentRangesAndNetworks(t *testing.T) {
	if _, err := Compare(Input{Reference: full(10, 12), Candidate: full(10, 13)}); err == nil {
		t.Error("different requested ranges must be refused")
	}
	a, b := full(10, 12), full(10, 12)
	a.Header.Network, b.Header.Network = "Test SDF Network ; September 2015", "Public Global Stellar Network ; September 2015"
	if _, err := Compare(Input{Reference: a, Candidate: b}); err == nil {
		t.Error("different networks must be refused")
	}
	if _, err := Compare(Input{}); err == nil {
		t.Error("missing streams must be refused")
	}
}

func TestResultIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	ps := []model.Payment{pay(12, 3, 3, 0, "3.0000000"), pay(10, 1, 1, 0, "1.0000000"), pay(11, 2, 2, 1, "2.0000000"), pay(11, 2, 2, 0, "9.0000000")}
	rev := []model.Payment{ps[3], ps[2], ps[1], ps[0]}
	cand := func(order []model.Payment) *model.Stream { return full(10, 12, order[1], order[2]) }
	a, _ := json.Marshal(run(t, full(10, 12, ps...), cand(ps)))
	b, _ := json.Marshal(run(t, full(10, 12, rev...), cand(rev)))
	if string(a) != string(b) {
		t.Fatalf("results differ:\n%s\n%s", a, b)
	}
}
