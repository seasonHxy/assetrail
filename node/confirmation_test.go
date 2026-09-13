package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
)

type gatedStore struct {
	blockStore
	entered chan struct{}
	release chan struct{}
	fail    bool
}

func (s gatedStore) Append(b blockchain.Block) error {
	close(s.entered)
	<-s.release
	if s.fail {
		return errors.New("injected disk failure")
	}
	return s.blockStore.Append(b)
}

func TestWaitReceiptDurableBoundary(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "disk failure"}[fail], func(t *testing.T) {
			key, _, bob, g := setup(t)
			n, err := Open(t.TempDir(), g)
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			tx := txFor(t, key, bob, 0)
			if _, err := n.Submit(tx); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results := make(chan error, 16)
			for i := 0; i < cap(results); i++ {
				go func() {
					r, err := n.WaitReceipt(ctx, tx.Hash())
					if err == nil && (r == nil || r.TransactionHash != tx.Hash()) {
						err = errors.New("wrong receipt")
					}
					results <- err
				}()
			}
			gate := gatedStore{blockStore: n.store, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			n.store = gate
			done := make(chan error, 1)
			go func() { _, err := n.Produce(time.Unix(1, 0)); done <- err }()
			<-gate.entered
			select {
			case err := <-results:
				t.Fatalf("acknowledged before durable write: %v", err)
			default:
			}
			close(gate.release)
			if err := <-done; (err != nil) != fail {
				t.Fatal(err)
			}
			for i := 0; i < cap(results); i++ {
				if err := <-results; (err != nil) != fail {
					t.Fatal("wait result", err)
				}
			}
			if !fail {
				r, err := n.Receipt(tx.Hash())
				if err != nil || r == nil {
					t.Fatal(err)
				}
				r.BlockHeight = 99
				r, _ = n.Receipt(tx.Hash())
				if r.BlockHeight != 1 {
					t.Fatal("receipt aliases index")
				}
				info, _ := n.Transaction(tx.Hash())
				info.Transaction.Signature[0] ^= 1
				info, _ = n.Transaction(tx.Hash())
				if info.Transaction.Validate(g.ChainID) != nil {
					t.Fatal("transaction aliases history")
				}
				header, _ := n.Header(1)
				block, _ := n.Block(1)
				if len(header.Transactions) != 0 || header.Hash() != block.Hash() {
					t.Fatal("incorrect lightweight header")
				}
			}
		})
	}
}

func TestWaitReceiptCancellationCloseAndFastTicks(t *testing.T) {
	key, _, bob, g := setup(t)
	n, err := Open(t.TempDir(), g)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := n.WaitReceipt(ctx, crypto.Hash{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	tx := txFor(t, key, bob, 0)
	if _, err := n.Submit(tx); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(runCtx, MinBlockInterval) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	if _, err := n.WaitReceipt(waitCtx, tx.Hash()); err != nil {
		t.Fatal(err)
	}
	header, _ := n.Header(^uint64(0))
	if _, err := n.Submit(txFor(t, key, bob, 1)); err != nil {
		t.Fatal(err)
	}
	if b, err := n.Produce(time.Unix(header.Header.Timestamp, 999999999)); err != nil || b != nil {
		t.Fatal("same-second block produced", err)
	}
	closed := make(chan error, 1)
	go func() { _, err := n.WaitReceipt(waitCtx, crypto.Hash{}); closed <- err }()
	stop()
	// The producer must exit before closing its node.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	done <- nil
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close failed to wake waiter", err)
	}
}
