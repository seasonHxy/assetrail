package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gochain/blockchain"
	"gochain/crypto"
)

func fixture(t testing.TB) (Bundle, crypto.Hash, *crypto.PrivateKey) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := crypto.Address(key.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(other.PubKey().SerializeCompressed())
	chain, err := blockchain.NewChain(9001, map[string]uint64{alice: 1 << 60})
	if err != nil {
		t.Fatal(err)
	}
	txs := make([]blockchain.Transaction, 33)
	for i := range txs {
		txs[i], err = (blockchain.Transaction{ChainID: 9001, To: bob, Amount: 9007199254740993, Nonce: uint64(i), GasLimit: blockchain.TransferGas}).Sign(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	block, err := chain.BuildBlock(txs, 1)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := crypto.BuildProof(blockchain.TransactionLeaves(txs), 32)
	if err != nil {
		t.Fatal(err)
	}
	return New(txs[32], block.Header, proof), block.Hash(), key
}

func TestRoundTripAndIndependentVerification(t *testing.T) {
	b, anchor, _ := fixture(t)
	var encoded bytes.Buffer
	if err := Encode(&encoded, b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"amount": "9007199254740993"`) {
		t.Fatal("amount lost its string encoding")
	}
	decoded, err := Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Verify(decoded, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Amount != 9007199254740993 || result.Scope != "signed-transaction-inclusion" {
		t.Fatal(result)
	}
}

func TestTamperingAndUntrustedRoots(t *testing.T) {
	original, anchor, key := fixture(t)
	for name, mutate := range map[string]func(*Bundle){
		"amount":    func(b *Bundle) { b.Transaction.Amount++ },
		"nonce":     func(b *Bundle) { b.Transaction.Nonce++ },
		"signature": func(b *Bundle) { b.Transaction.Signature[0] ^= 1 },
		"recipient": func(b *Bundle) { b.Transaction.To = b.Transaction.From },
		"path":      func(b *Bundle) { b.Proof.Siblings[0][0] ^= 1 },
		"index":     func(b *Bundle) { b.Proof.Index++ },
		"count":     func(b *Bundle) { b.Proof.Total++ },
		"chain":     func(b *Bundle) { b.Header.ChainID++ },
		"root":      func(b *Bundle) { b.Header.TxRoot[0] ^= 1 },
		"version":   func(b *Bundle) { b.Version = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(original)
			b, err := Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&b)
			if _, err := Verify(b, anchor); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
	if _, err := Verify(original, crypto.Hash{}); err == nil {
		t.Fatal("missing trust anchor accepted")
	}
	// An attacker can build a self-consistent alternative tree. It still cannot
	// match the independently pinned legitimate block hash.
	tx := original.Transaction.Core()
	tx.Amount = 1
	tx, err := tx.Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	leaves := blockchain.TransactionLeaves([]blockchain.Transaction{tx})
	proof, _ := crypto.BuildProof(leaves, 0)
	header := original.Header.Core()
	header.TxRoot = crypto.MerkleRoot(leaves)
	fake := New(tx, header, proof)
	if _, err := Verify(fake, anchor); err == nil {
		t.Fatal("self-consistent alternative root bypassed anchor")
	}
	fakeAnchor := (blockchain.Block{Header: header}).Hash()
	if _, err := Verify(fake, fakeAnchor); err != nil {
		t.Fatal("test fixture not self-consistent", err)
	}
}

func TestDecoderBounds(t *testing.T) {
	for _, data := range []string{`{"unknown":1}`, `{} {}`, strings.Repeat(" ", MaxBundleBytes+1), `{"transaction":{"amount":9007199254740993}}`} {
		if _, err := Decode(strings.NewReader(data)); err == nil {
			t.Fatal("invalid bundle encoding accepted")
		}
	}
}
