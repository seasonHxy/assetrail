package mempool

import (
	"sync"
	"testing"

	"gochain/blockchain"
	"gochain/crypto"
)

func wallet(t *testing.T) (*crypto.PrivateKey, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address, err := crypto.Address(key.PubKey().SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}
	return key, address
}
func transaction(t *testing.T, key *crypto.PrivateKey, to string, amount, nonce, price uint64) blockchain.Transaction {
	t.Helper()
	tx, err := (blockchain.Transaction{ChainID: 9001, To: to, Amount: amount, Nonce: nonce, GasLimit: blockchain.TransferGas, GasPrice: price}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func state(t *testing.T, alloc map[string]uint64) *blockchain.State {
	t.Helper()
	s, err := blockchain.NewState(alloc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func pool(t *testing.T, capacity int) *Pool {
	t.Helper()
	p, err := New(9001, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func add(t *testing.T, p *Pool, tx blockchain.Transaction, s *blockchain.State) {
	t.Helper()
	if err := p.Add(tx, s); err != nil {
		t.Fatal(err)
	}
}

func TestNonceQueueReplacementAndReconcile(t *testing.T) {
	key, alice := wallet(t)
	_, bob := wallet(t)
	s := state(t, map[string]uint64{alice: 1_000_000})
	p := pool(t, 3)
	add(t, p, transaction(t, key, bob, 1, 2, 0), s)
	if pending, queued := p.Status(s); pending != 0 || queued != 1 {
		t.Fatal(pending, queued)
	}
	if len(p.Select(s, 10)) != 0 {
		t.Fatal("selected nonce gap")
	}
	first := transaction(t, key, bob, 1, 0, 10)
	add(t, p, first, s)
	if err := p.Add(first, s); err == nil {
		t.Fatal("accepted duplicate")
	}
	if err := p.Add(transaction(t, key, bob, 2, 0, 10), s); err == nil {
		t.Fatal("accepted underpriced replacement")
	}
	replacement := transaction(t, key, bob, 2, 0, 11)
	add(t, p, replacement, s)
	if _, ok := p.Find(first.Hash()); ok {
		t.Fatal("replaced hash retained")
	}
	if next, err := p.NextNonce(alice, 0); err != nil || next != 1 {
		t.Fatal("first gap", next, err)
	}
	found, ok := p.Find(replacement.Hash())
	if !ok {
		t.Fatal("replacement missing")
	}
	found.Signature[0] ^= 1
	found, _ = p.Find(replacement.Hash())
	if found.Validate(9001) != nil {
		t.Fatal("hash index aliases caller")
	}
	add(t, p, transaction(t, key, bob, 1, 1, 0), s)
	if next, err := p.NextNonce(alice, 0); err != nil || next != 3 {
		t.Fatal("pending nonce", next, err)
	}
	if pending, queued := p.Status(s); pending != 3 || queued != 0 {
		t.Fatal(pending, queued)
	}
	selected := p.Select(s, 10)
	if len(selected) != 3 || selected[0].Hash() != replacement.Hash() {
		t.Fatal("wrong replacement or selection")
	}
	for i, tx := range selected {
		if tx.Nonce != uint64(i) {
			t.Fatal("nonce order")
		}
		if err := s.Apply(tx, 9001); err != nil {
			t.Fatal(err)
		}
	}
	if p.Len() != 3 {
		t.Fatal("selection removed transactions before commit")
	}
	p.Reconcile(s)
	if _, ok := p.Find(replacement.Hash()); ok {
		t.Fatal("committed hash retained")
	}
	if p.Len() != 0 {
		t.Fatal("committed transactions retained")
	}
	if err := p.Add(first, s); err == nil {
		t.Fatal("accepted stale nonce")
	}
}

func TestPoolBoundsAndIsolation(t *testing.T) {
	key, alice := wallet(t)
	_, bob := wallet(t)
	s := state(t, map[string]uint64{alice: 1_000_000})
	p := pool(t, 1)
	tx := transaction(t, key, bob, 1, 0, 0)
	add(t, p, tx, s)
	tx.Signature[0] ^= 1
	if len(p.Select(s, 1)) != 1 {
		t.Fatal("pool aliases admitted signature")
	}
	selected := p.Select(s, 1)
	selected[0].Signature[0] ^= 1
	if len(p.Select(s, 1)) != 1 {
		t.Fatal("selection aliases pool")
	}
	if p.Add(transaction(t, key, bob, 1, 1, 0), s) == nil {
		t.Fatal("capacity ignored")
	}
	add(t, p, transaction(t, key, bob, 1, 0, 1), s) // Replacement is allowed at capacity.
	if p.Len() != 1 {
		t.Fatal("replacement changed size")
	}
	if p.Add(transaction(t, key, bob, 1, MaxNonceGap+1, 0), s) == nil {
		t.Fatal("nonce window ignored")
	}
	if len(p.Select(s, 0)) != 0 || len(p.Select(s, -1)) != 0 {
		t.Fatal("invalid selection limit")
	}
	other := pool(t, 10)
	bad := transaction(t, key, bob, 1, 0, 0)
	bad.Amount++
	if other.Add(bad, s) == nil {
		t.Fatal("accepted invalid signature")
	}
	bad = transaction(t, key, bob, 1, 0, 0)
	bad.ChainID = 9002
	bad, err := bad.Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	if other.Add(bad, s) == nil {
		t.Fatal("accepted another chain's transaction")
	}
}

func TestSelectionFundsDependenciesAndAvoidsOverspend(t *testing.T) {
	aliceKey, alice := wallet(t)
	bobKey, bob := wallet(t)
	_, carol := wallet(t)
	s := state(t, map[string]uint64{alice: 100_000})
	p := pool(t, 10)
	// Bob's high-price transaction initially cannot execute. Alice funds Bob.
	add(t, p, transaction(t, bobKey, carol, 1, 0, 1), s)
	add(t, p, transaction(t, aliceKey, bob, 30_000, 0, 0), s)
	add(t, p, transaction(t, aliceKey, carol, 80_000, 1, 0), s)
	root := s.Root()
	selected := p.Select(s, 10)
	if len(selected) != 2 || selected[0].From != alice || selected[1].From != bob {
		t.Fatal("dependency selection failed")
	}
	if s.Root() != root {
		t.Fatal("selection changed input state")
	}
	if p.Len() != 3 {
		t.Fatal("selection removed entries")
	}
}

func TestSelectionDeterministicAcrossInsertionOrder(t *testing.T) {
	ak, a := wallet(t)
	bk, b := wallet(t)
	_, c := wallet(t)
	s := state(t, map[string]uint64{a: 1_000_000, b: 1_000_000})
	txs := []blockchain.Transaction{transaction(t, ak, c, 1, 0, 0), transaction(t, bk, c, 1, 0, 0), transaction(t, ak, c, 1, 1, 5)}
	p, q := pool(t, 10), pool(t, 10)
	for _, tx := range txs {
		add(t, p, tx, s)
	}
	for i := len(txs) - 1; i >= 0; i-- {
		add(t, q, txs[i], s)
	}
	x, y := p.Select(s, 10), q.Select(s, 10)
	if len(x) != 3 || len(y) != 3 {
		t.Fatal("missing transactions")
	}
	for i := range x {
		if x[i].Hash() != y[i].Hash() {
			t.Fatal("nondeterministic selection")
		}
	}
}

func TestConcurrentPoolAccess(t *testing.T) {
	key, alice := wallet(t)
	_, bob := wallet(t)
	s := state(t, map[string]uint64{alice: 1_000_000})
	p := pool(t, 100)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		tx := transaction(t, key, bob, 1, uint64(i), 0)
		wg.Add(1)
		go func(tx blockchain.Transaction) {
			defer wg.Done()
			if err := p.Add(tx, s); err != nil {
				t.Error(err)
			}
			p.Len()
			p.Status(s)
			p.Select(s, 2)
		}(tx)
	}
	wg.Wait()
	if p.Len() != 20 {
		t.Fatal("lost concurrent admissions")
	}
}
