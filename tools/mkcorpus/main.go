// Command mkcorpus submits a fixed set of real transactions to Stellar testnet and writes what it
// built as ground truth. The ground truth comes from the transactions this tool constructed, not
// from any adapter or comparator, so it is an independent reference for the recorded corpus.
//
// Keys are generated in memory for each run and are never written to disk.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
)

const (
	horizon   = "https://horizon-testnet.stellar.org"
	friendbot = "https://friendbot.stellar.org"
)

type groundOp struct {
	Index       int    `json:"index"`
	Type        string `json:"type"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	ToMuxedID   string `json:"toMuxedId,omitempty"`
	AssetType   string `json:"assetType,omitempty"`
	AssetCode   string `json:"assetCode,omitempty"`
	AssetIssuer string `json:"assetIssuer,omitempty"`
	Amount      string `json:"amount,omitempty"`
}

type groundTx struct {
	Label            string     `json:"label"`
	Hash             string     `json:"hash"`
	Ledger           uint32     `json:"ledger"`
	ApplicationOrder uint32     `json:"applicationOrder"`
	Successful       bool       `json:"successful"`
	FeeBump          bool       `json:"feeBump"`
	Ops              []groundOp `json:"ops"`
}

type groundTruth struct {
	Network string     `json:"network"`
	Created string     `json:"created"`
	Issuer  string     `json:"issuer"`
	Alice   string     `json:"alice"`
	Bob     string     `json:"bob"`
	Txs     []groundTx `json:"txs"`
}

var client = &http.Client{Timeout: 30 * time.Second}

func getJSON(u string, out any) error {
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %d %s", u, resp.StatusCode, string(b))
	}
	return json.Unmarshal(b, out)
}

func fund(addr string) error {
	resp, err := client.Get(friendbot + "/?addr=" + url.QueryEscape(addr))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("friendbot: %d %s", resp.StatusCode, string(b))
	}
	return nil
}

func sequence(addr string) (int64, error) {
	var a struct {
		Sequence string `json:"sequence"`
	}
	if err := getJSON(horizon+"/accounts/"+addr, &a); err != nil {
		return 0, err
	}
	return strconv.ParseInt(a.Sequence, 10, 64)
}

// submit posts an envelope. A transaction that is included but fails (HTTP 400 tx_failed) is expected for the failure case.
func submit(b64 string) (int, string) {
	resp, err := client.PostForm(horizon+"/transactions", url.Values{"tx": {b64}})
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitTx(hash string) (ledger uint32, order uint32, ok bool, err error) {
	for i := 0; i < 20; i++ {
		var t struct {
			Ledger      uint32 `json:"ledger"`
			Successful  bool   `json:"successful"`
			PagingToken string `json:"paging_token"`
		}
		if err = getJSON(horizon+"/transactions/"+hash, &t); err == nil {
			pt, _ := strconv.ParseUint(t.PagingToken, 10, 64)
			return t.Ledger, uint32((pt >> 12) & 0xFFFFF), t.Successful, nil
		}
		time.Sleep(2 * time.Second)
	}
	return 0, 0, false, err
}

func must[T any](v T, err error) T {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	return v
}

func main() {
	out := "ground-truth.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	_ = context.Background()
	pass := network.TestNetworkPassphrase
	issuer, alice, bob := must(keypair.Random()), must(keypair.Random()), must(keypair.Random())
	for _, k := range []*keypair.Full{issuer, alice, bob} {
		if err := fund(k.Address()); err != nil {
			fmt.Fprintln(os.Stderr, "fund:", err)
			os.Exit(1)
		}
	}
	time.Sleep(6 * time.Second)

	usdx := txnbuild.CreditAsset{Code: "USDX", Issuer: issuer.Address()}
	seqs := map[string]int64{}
	for _, k := range []*keypair.Full{issuer, alice, bob} {
		seqs[k.Address()] = must(sequence(k.Address()))
	}
	gt := groundTruth{Network: pass, Created: time.Now().UTC().Format(time.RFC3339), Issuer: issuer.Address(), Alice: alice.Address(), Bob: bob.Address()}

	// run builds, signs and submits one transaction and records its ground truth.
	run := func(label string, source *keypair.Full, ops []txnbuild.Operation, truth []groundOp, signers ...*keypair.Full) {
		seqs[source.Address()]++
		acct := txnbuild.NewSimpleAccount(source.Address(), seqs[source.Address()]-1)
		tx := must(txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount: &acct, IncrementSequenceNum: true, Operations: ops, BaseFee: txnbuild.MinBaseFee * 10,
			Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
		}))
		tx = must(tx.Sign(pass, append([]*keypair.Full{source}, signers...)...))
		hash := must(tx.HashHex(pass))
		code, body := submit(must(tx.Base64()))
		fmt.Printf("%-28s submit=%d hash=%s\n", label, code, hash)
		_ = body
		ledger, order, ok, err := waitTx(hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tx %s not found after submit: %v\n%s\n", label, err, body)
			os.Exit(1)
		}
		gt.Txs = append(gt.Txs, groundTx{Label: label, Hash: hash, Ledger: ledger, ApplicationOrder: order, Successful: ok, Ops: truth})
	}

	nat := txnbuild.NativeAsset{}
	pay := func(src, dst string, a txnbuild.Asset, amt string) *txnbuild.Payment {
		return &txnbuild.Payment{SourceAccount: src, Destination: dst, Asset: a, Amount: amt}
	}
	ground := func(i int, from, to string, a txnbuild.Asset, amt string) groundOp {
		g := groundOp{Index: i, Type: "payment", From: from, To: to, Amount: amt}
		if a.IsNative() {
			g.AssetType = "native"
		} else {
			g.AssetType, g.AssetCode, g.AssetIssuer = "credit", a.GetCode(), a.GetIssuer()
		}
		return g
	}

	run("trustlines", alice, []txnbuild.Operation{
		&txnbuild.ChangeTrust{Line: usdx.MustToChangeTrustAsset(), Limit: "100000"},
		&txnbuild.ChangeTrust{SourceAccount: bob.Address(), Line: usdx.MustToChangeTrustAsset(), Limit: "100000"},
	}, []groundOp{{Index: 0, Type: "change_trust"}, {Index: 1, Type: "change_trust"}}, bob)

	run("issue-usdx", issuer, []txnbuild.Operation{pay("", alice.Address(), usdx, "1000.0000000")},
		[]groundOp{ground(0, issuer.Address(), alice.Address(), usdx, "1000.0000000")})

	run("native-seven-decimals", alice, []txnbuild.Operation{pay("", bob.Address(), nat, "12.3456789")},
		[]groundOp{ground(0, alice.Address(), bob.Address(), nat, "12.3456789")})

	run("issued-asset-payment", alice, []txnbuild.Operation{pay("", bob.Address(), usdx, "5.0000000")},
		[]groundOp{ground(0, alice.Address(), bob.Address(), usdx, "5.0000000")})

	run("multi-op-with-unsupported", alice, []txnbuild.Operation{
		pay("", bob.Address(), nat, "1.5000000"),
		pay("", bob.Address(), usdx, "2.0000000"),
		&txnbuild.ManageData{Name: "eventparity", Value: []byte("corpus")},
		pay("", bob.Address(), nat, "0.0000001"),
	}, []groundOp{
		ground(0, alice.Address(), bob.Address(), nat, "1.5000000"),
		ground(1, alice.Address(), bob.Address(), usdx, "2.0000000"),
		{Index: 2, Type: "manage_data"},
		ground(3, alice.Address(), bob.Address(), nat, "0.0000001"),
	})

	run("path-payment-unsupported", alice, []txnbuild.Operation{
		&txnbuild.PathPaymentStrictSend{SendAsset: nat, SendAmount: "3.0000000", Destination: bob.Address(), DestAsset: nat, DestMin: "3.0000000"},
	}, []groundOp{{Index: 0, Type: "path_payment_strict_send"}})

	run("failed-underfunded", alice, []txnbuild.Operation{pay("", bob.Address(), nat, "999999999.0000000")},
		[]groundOp{ground(0, alice.Address(), bob.Address(), nat, "999999999.0000000")})

	var muxed strkey.MuxedAccount
	must(0, muxed.SetAccountID(bob.Address()))
	muxed.SetID(42)
	mAddr := must(muxed.Address())
	mu := ground(0, alice.Address(), bob.Address(), nat, "0.5000000")
	mu.ToMuxedID = "42"
	run("muxed-destination", alice, []txnbuild.Operation{pay("", mAddr, nat, "0.5000000")}, []groundOp{mu})

	// Fee bump: bob pays the fee for an inner transaction from alice.
	seqs[alice.Address()]++
	acct := txnbuild.NewSimpleAccount(alice.Address(), seqs[alice.Address()]-1)
	inner := must(txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &acct, IncrementSequenceNum: true, BaseFee: txnbuild.MinBaseFee,
		Operations:    []txnbuild.Operation{pay("", bob.Address(), nat, "0.2500000")},
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	}))
	inner = must(inner.Sign(pass, alice))
	fb := must(txnbuild.NewFeeBumpTransaction(txnbuild.FeeBumpTransactionParams{Inner: inner, FeeAccount: bob.Address(), BaseFee: txnbuild.MinBaseFee * 10}))
	fb = must(fb.Sign(pass, bob))
	fbHash := must(fb.HashHex(pass))
	code, body := submit(must(fb.Base64()))
	fmt.Printf("%-28s submit=%d hash=%s\n", "fee-bump", code, fbHash)
	l, o, ok, err := waitTx(fbHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fee bump not found: %v\n%s\n", err, body)
		os.Exit(1)
	}
	gt.Txs = append(gt.Txs, groundTx{Label: "fee-bump", Hash: fbHash, Ledger: l, ApplicationOrder: o, Successful: ok, FeeBump: true,
		Ops: []groundOp{ground(0, alice.Address(), bob.Address(), nat, "0.2500000")}})

	b, _ := json.MarshalIndent(gt, "", "  ")
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lo, hi := gt.Txs[0].Ledger, gt.Txs[0].Ledger
	for _, t := range gt.Txs {
		if t.Ledger < lo {
			lo = t.Ledger
		}
		if t.Ledger > hi {
			hi = t.Ledger
		}
	}
	fmt.Printf("wrote %s; ledgers %d..%d (%d)\n", out, lo, hi, hi-lo+1)
	_ = strings.TrimSpace
}
