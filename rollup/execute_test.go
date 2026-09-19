package rollup

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"

	"gochain/crypto"
)

func fixture(t *testing.T) (Batch, []*crypto.PrivateKey) {
	t.Helper()
	keys := []*crypto.PrivateKey{}
	c := Config{L1ChainID: 31337, Contract: "0x1111111111111111111111111111111111111111"}
	for i := byte(1); i <= 2; i++ {
		raw := make([]byte, 32)
		raw[31] = i
		k, err := crypto.ParsePrivateKey(raw)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
		c.Accounts = append(c.Accounts, EthereumAddress(k))
		t.Cleanup(k.Zero)
	}
	b := Batch{Config: c, Sequence: 1, Deposits: []Deposit{{Account: 0, Asset: 1, Amount: 100}, {Account: 1, Asset: 0, Amount: 1000}}}
	o := Operation{Kind: DvP, From: 0, To: 1, Amount: 10, Price: 250, Sequence: 1}
	if err := o.Sign(c, keys[0], false); err != nil {
		t.Fatal(err)
	}
	if err := o.Sign(c, keys[1], true); err != nil {
		t.Fatal(err)
	}
	b.Operations = []Operation{o}
	return b, keys
}
func TestDvPAtomicityAndSolidityEncoding(t *testing.T) {
	b, _ := fixture(t)
	before := b.Previous
	r, err := Execute(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.State.Cash[0] != 250 || r.State.Cash[1] != 750 || r.State.RWA[0] != 90 || r.State.RWA[1] != 10 || r.State.Nonce[0] != 1 || r.State.Nonce[1] != 1 {
		t.Fatal("incorrect DvP")
	}
	if b.Previous != before || len(r.State.ABI()) != 768 {
		t.Fatal("state mutation/encoding")
	}
	raw, err := hex.DecodeString(r.Data[2:])
	if err != nil {
		t.Fatal(err)
	}
	ops, err := DecodeOperations(raw)
	if err != nil {
		t.Fatal(err)
	}
	b.Operations = ops
	decoded, err := Execute(b)
	if err != nil || decoded.PostRoot != r.PostRoot {
		t.Fatal("wire roundtrip", err)
	}
}
func TestInvalidBatchesReleaseNoPartialResult(t *testing.T) {
	for _, name := range []string{"cash", "rwa", "nonce", "sequence", "domain", "signature", "counter", "overflow", "withdraw-recipient"} {
		t.Run(name, func(t *testing.T) {
			b, keys := fixture(t)
			o := b.Operations[0]
			switch name {
			case "cash":
				o.Price = 1001
			case "rwa":
				o.Amount = 101
			case "nonce":
				o.Nonce = 1
			case "sequence":
				o.Sequence = 2
			case "domain":
				b.Config.L1ChainID++
			case "signature":
				o.Signature = "0x" + hex.EncodeToString(make([]byte, 65))
			case "counter":
				o.CounterSignature = o.Signature
			case "overflow":
				b.Previous.Cash[1] = Quantity(math.MaxUint64)
			case "withdraw-recipient":
				o = Operation{Kind: WithdrawRWA, From: 0, To: 1, Amount: 1, Sequence: 1}
			}
			if name != "domain" && name != "signature" && name != "counter" {
				if err := o.Sign(b.Config, keys[0], false); err != nil {
					t.Fatal(err)
				}
				if o.Kind == DvP {
					if err := o.Sign(b.Config, keys[1], true); err != nil {
						t.Fatal(err)
					}
				}
			}
			b.Operations[0] = o
			before := b.Previous
			r, err := Execute(b)
			if err == nil || r != (Result{}) || b.Previous != before {
				t.Fatal("invalid batch published state")
			}
		})
	}
}
func TestReplayAndSelfPayment(t *testing.T) {
	b, keys := fixture(t)
	r, err := Execute(b)
	if err != nil {
		t.Fatal(err)
	}
	replay := b
	replay.Previous = r.State
	replay.Deposits = nil
	replay.Sequence = 2
	if _, err := Execute(replay); err == nil {
		t.Fatal("replay accepted")
	}
	op := Operation{Kind: Pay, From: 0, To: 0, Amount: 250, Nonce: 1, Sequence: 2}
	if err := op.Sign(b.Config, keys[0], false); err != nil {
		t.Fatal(err)
	}
	replay.Operations = []Operation{op}
	r2, err := Execute(replay)
	if err != nil || r2.State.Cash != r.State.Cash || r2.State.Nonce[0] != 2 {
		t.Fatal("self payment", err)
	}
}
func TestQuantityStrictAndEthereumAddress(t *testing.T) {
	b, _ := fixture(t)
	if b.Config.Accounts[0] != "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf" {
		t.Fatal("Ethereum address derivation")
	}
	for _, input := range []string{`1`, `"01"`, `"-1"`, `"18446744073709551616"`} {
		var q Quantity
		if json.Unmarshal([]byte(input), &q) == nil {
			t.Fatal("bad quantity", input)
		}
	}
	var q Quantity
	if err := json.Unmarshal([]byte(`"18446744073709551615"`), &q); err != nil || q != Quantity(math.MaxUint64) {
		t.Fatal("precision")
	}
}
