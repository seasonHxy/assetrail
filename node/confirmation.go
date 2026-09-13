package node

import (
	"context"
	"fmt"

	"gochain/blockchain"
	"gochain/crypto"
)

// notify runs with n.mu held, after publication or when the node stops serving.
func (n *Node) notify(commit bool) {
	close(n.changed)
	n.changed = make(chan struct{})
	if commit {
		close(n.committed)
		n.committed = make(chan struct{})
	}
}

// Changes returns a broadcast generation for the P2P repair loop. Capture it
// before inspecting/exchanging state so changes during an exchange aren't lost.
func (n *Node) Changes() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.changed
}

// Receipt reads the durable commit index without looking up or cloning a block.
func (n *Node) Receipt(hash crypto.Hash) (*Receipt, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if receipt, ok := n.receipts[hash]; ok {
		return &receipt, nil
	}
	return nil, nil
}

// WaitReceipt waits for this node's durable commit, not quorum finality. The
// condition and broadcast generation are read under one lock to avoid lost wakeups.
func (n *Node) WaitReceipt(ctx context.Context, hash crypto.Hash) (*Receipt, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n.mu.Lock()
		err := n.check()
		receipt, exists := n.receipts[hash]
		changed := n.committed
		n.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if exists {
			return &receipt, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (n *Node) PendingNonce(address string) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return 0, err
	}
	if !crypto.ValidAddress(address) {
		return 0, fmt.Errorf("invalid address")
	}
	return n.pool.NextNonce(address, n.chain.Account(address).Nonce)
}

func (n *Node) Header(height uint64) (*blockchain.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	header, ok := n.chain.Header(height)
	if !ok {
		return nil, nil
	}
	return &blockchain.Block{Header: header}, nil
}
