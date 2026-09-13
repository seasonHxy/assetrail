package blockchain

import (
	"fmt"
	"math"
	"sync"

	"gochain/crypto"
)

const MaxBlockTransactions = 1000

// Chain is a single-node, in-memory chain. Append is validation, not consensus.
type Chain struct {
	mu     sync.RWMutex
	id     uint64
	state  *State
	blocks []Block
}

func NewChain(chainID uint64, alloc map[string]uint64) (*Chain, error) {
	if chainID == 0 {
		return nil, fmt.Errorf("chain ID must be nonzero")
	}
	s, err := NewState(alloc)
	if err != nil {
		return nil, err
	}
	genesis := Block{Header: BlockHeader{ChainID: chainID, TxRoot: crypto.MerkleRoot(nil), StateRoot: s.Root()}, Transactions: []Transaction{}}
	return &Chain{id: chainID, state: s, blocks: []Block{genesis}}, nil
}
func (c *Chain) Snapshot() *State { c.mu.RLock(); defer c.mu.RUnlock(); return c.state.Clone() }

// Value-only accessors avoid copying the entire block/state for small queries.
func (c *Chain) Account(address string) Account {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state.Account(address)
}

func (c *Chain) Header(height uint64) (BlockHeader, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if height == math.MaxUint64 {
		height = uint64(len(c.blocks) - 1)
	}
	if height >= uint64(len(c.blocks)) {
		return BlockHeader{}, false
	}
	return c.blocks[height].Header, true
}

func (c *Chain) TransactionAt(height, index uint64) (Transaction, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if height >= uint64(len(c.blocks)) || index >= uint64(len(c.blocks[height].Transactions)) {
		return Transaction{}, false
	}
	return c.blocks[height].Transactions[index].Clone(), true
}
func (c *Chain) Tip() Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blocks[len(c.blocks)-1].Clone()
}

func execute(state *State, txs []Transaction, chainID uint64) (*State, error) {
	if len(txs) > MaxBlockTransactions {
		return nil, fmt.Errorf("too many transactions")
	}
	next := state.Clone()
	for i, tx := range txs {
		if err := next.Apply(tx, chainID); err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
	}
	return next, nil
}

func (c *Chain) BuildBlock(txs []Transaction, timestamp int64) (Block, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tip := c.blocks[len(c.blocks)-1]
	if timestamp <= tip.Header.Timestamp {
		return Block{}, fmt.Errorf("timestamp must increase")
	}
	if tip.Header.Height == math.MaxUint64 {
		return Block{}, fmt.Errorf("height exhausted")
	}
	block := Block{Transactions: txs}.Clone()
	next, err := execute(c.state, block.Transactions, c.id)
	if err != nil {
		return Block{}, err
	}
	block.Header = BlockHeader{ChainID: c.id, Height: tip.Header.Height + 1, ParentHash: tip.Hash(), Timestamp: timestamp,
		TxRoot: crypto.MerkleRoot(TransactionLeaves(block.Transactions)), StateRoot: next.Root()}
	return block, nil
}

// Append re-executes against a private snapshot and only commits a fully valid block.
func (c *Chain) Append(block Block) error {
	return c.AppendWithCommit(block, nil)
}

// AppendWithCommit validates first, invokes durableCommit under the chain lock,
// then publishes the new state. The callback must not call back into Chain.
// A failed durable commit leaves memory unchanged; an ambiguous disk failure
// requires the caller to stop writing and reopen from storage.
func (c *Chain) AppendWithCommit(block Block, durableCommit func(Block) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	block = block.Clone()
	next, err := c.validateLocked(block)
	if err != nil {
		return err
	}
	if durableCommit != nil {
		if err := durableCommit(block.Clone()); err != nil {
			return fmt.Errorf("persist block: %w", err)
		}
	}
	c.state = next
	c.blocks = append(c.blocks, block)
	return nil
}

func (c *Chain) Block(height uint64) (Block, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if height >= uint64(len(c.blocks)) {
		return Block{}, false
	}
	return c.blocks[height].Clone(), true
}

// Validate checks a candidate against committed state without publishing it.
func (c *Chain) Validate(block Block) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, err := c.validateLocked(block.Clone())
	return err
}
func (c *Chain) validateLocked(block Block) (*State, error) {
	tip := c.blocks[len(c.blocks)-1]
	if block.Header.ChainID != c.id || tip.Header.Height == math.MaxUint64 || block.Header.Height != tip.Header.Height+1 || block.Header.ParentHash != tip.Hash() {
		return nil, fmt.Errorf("block does not extend chain")
	}
	if block.Header.Timestamp <= tip.Header.Timestamp {
		return nil, fmt.Errorf("timestamp must increase")
	}
	if len(block.Transactions) > MaxBlockTransactions {
		return nil, fmt.Errorf("too many transactions")
	}
	if crypto.MerkleRoot(TransactionLeaves(block.Transactions)) != block.Header.TxRoot {
		return nil, fmt.Errorf("transaction root mismatch")
	}
	next, err := execute(c.state, block.Transactions, c.id)
	if err != nil {
		return nil, err
	}
	if next.Root() != block.Header.StateRoot {
		return nil, fmt.Errorf("state root mismatch")
	}
	return next, nil
}
