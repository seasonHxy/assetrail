package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/storage"
)

func setup(t *testing.T) (*crypto.PrivateKey, string, string, blockchain.Genesis) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := crypto.Address(key.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(bobKey.PubKey().SerializeCompressed())
	return key, alice, bob, blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{alice: 1000000}}
}
func txFor(t *testing.T, key *crypto.PrivateKey, to string, nonce uint64) blockchain.Transaction {
	t.Helper()
	tx, err := (blockchain.Transaction{ChainID: 9001, To: to, Amount: 20, Nonce: nonce, GasLimit: blockchain.TransferGas}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestRestartRecoversBlocksReceiptsAndProofs(t *testing.T) {
	key, alice, bob, g := setup(t)
	dir := t.TempDir()
	n, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	tx := txFor(t, key, bob, 0)
	if _, err := n.Submit(tx); err != nil {
		t.Fatal(err)
	}
	pending, err := n.Transaction(tx.Hash())
	if err != nil || pending == nil || pending.Receipt != nil {
		t.Fatal("missing pending transaction", err)
	}
	b, err := n.Produce(time.Unix(10, 0))
	if err != nil || b == nil {
		t.Fatal(err)
	}
	// Pending nonce 1 deliberately is not durable until committed.
	if _, err := n.Submit(txFor(t, key, bob, 1)); err != nil {
		t.Fatal(err)
	}
	before, _ := n.Status()
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	n, err = Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := n.Status()
	if after.Height != 1 || after.StateRoot != before.StateRoot || after.BlockHash != before.BlockHash || after.Pending != 0 {
		t.Fatal("incorrect recovery", after)
	}
	account, _ := n.Account(alice)
	if account.Balance != 999980 || account.Nonce != 1 {
		t.Fatal(account)
	}
	info, err := n.Transaction(tx.Hash())
	if err != nil || info == nil || info.Receipt == nil || info.Receipt.BlockHash != b.Hash() {
		t.Fatal("missing restored receipt", err)
	}
	proof, err := n.Proof(tx.Hash())
	if err != nil || proof == nil {
		t.Fatal(err)
	}
	hash := tx.Hash()
	if !crypto.VerifyProof(hash[:], proof.Proof, proof.Root) {
		t.Fatal("invalid recovered proof")
	}
	if _, err := n.Submit(tx); err == nil {
		t.Fatal("replay accepted after restart")
	}
	if empty, err := n.Produce(time.Unix(11, 0)); err != nil || empty != nil {
		t.Fatal("produced empty block")
	}
}

func TestExclusiveDirectoryAndGenesisValidation(t *testing.T) {
	_, alice, _, g := setup(t)
	dir := t.TempDir()
	n, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Open(dir, g); err == nil {
		_ = second.Close()
		t.Fatal("opened locked directory")
	}
	_ = n.Close()
	wrong := blockchain.Genesis{ChainID: g.ChainID, Alloc: map[string]uint64{alice: 42}}
	if second, err := Open(dir, wrong); err == nil {
		_ = second.Close()
		t.Fatal("wrong genesis accepted")
	}
	n, err = Open(dir, g)
	if err != nil {
		t.Fatal("failed open did not release lock", err)
	}
	_ = n.Close()
}

func TestCorruptedHistoryRejectedWithoutReset(t *testing.T) {
	key, _, bob, g := setup(t)
	dir := t.TempDir()
	n, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = n.Submit(txFor(t, key, bob, 0))
	if _, err := n.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	path := filepath.Join(dir, "chain.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot storage.Snapshot
	if err := json.Unmarshal(original, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Blocks[0].Header.StateRoot[0] ^= 1
	corrupt, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := Open(dir, g); err == nil {
		_ = n.Close()
		t.Fatal("corrupt history accepted")
	}
	stored, _ := os.ReadFile(path)
	if string(stored) != string(corrupt) {
		t.Fatal("corrupted store was silently replaced")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	n, err = Open(dir, g)
	if err != nil {
		t.Fatal("replay failure leaked lock", err)
	}
	_ = n.Close()
}

type failingStore struct{ blockStore }

func (f failingStore) Append(blockchain.Block) error { return errors.New("injected disk failure") }

func TestPersistenceFailureNeverPublishesState(t *testing.T) {
	key, _, bob, g := setup(t)
	n, err := Open(t.TempDir(), g)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	n.store = failingStore{n.store}
	_, _ = n.Submit(txFor(t, key, bob, 0))
	root := n.chain.Snapshot().Root()
	if _, err := n.Produce(time.Unix(1, 0)); err == nil {
		t.Fatal("disk failure ignored")
	}
	if n.chain.Tip().Header.Height != 0 || n.chain.Snapshot().Root() != root || n.pool.Len() != 1 {
		t.Fatal("failed commit changed memory")
	}
	if _, err := n.Status(); err == nil {
		t.Fatal("faulted node still serves")
	}
	if _, err := n.Submit(txFor(t, key, bob, 1)); err == nil {
		t.Fatal("faulted node accepts writes")
	}
}

func TestConcurrentAdmissionAndProduction(t *testing.T) {
	key, alice, bob, g := setup(t)
	n, err := Open(t.TempDir(), g)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		tx := txFor(t, key, bob, uint64(i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := n.Submit(tx); err != nil {
				t.Error(err)
			}
			_, _ = n.Status()
			_, _ = n.Produce(time.Unix(10, 0))
		}()
	}
	wg.Wait()
	if _, err := n.Produce(time.Unix(11, 0)); err != nil {
		t.Fatal(err)
	}
	a, _ := n.Account(alice)
	if a.Nonce != 16 || a.Balance != 1000000-16*20 {
		t.Fatal(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Run(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := n.Run(ctx, time.Millisecond); err == nil {
		t.Fatal("invalid interval accepted")
	}
}
