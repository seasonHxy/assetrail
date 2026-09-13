// Package node serializes transaction admission, block production and durable
// commits. This producer is single-node execution, not a consensus protocol.
package node

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"gochain/blockchain"
	"gochain/consensus"
	"gochain/crypto"
	"gochain/mempool"
	"gochain/storage"
)

type blockStore interface {
	Append(blockchain.Block) error
	Close() error
}

const MinBlockInterval = 50 * time.Millisecond

type Node struct {
	engineMu     sync.Mutex
	engine       *consensus.Engine
	validators   *consensus.ValidatorSet
	certificates map[uint64]consensus.Certificate
	mu           sync.Mutex
	chain        *blockchain.Chain
	pool         *mempool.Pool
	store        blockStore
	receipts     map[crypto.Hash]Receipt
	fault        error
	closed       bool
	mode         string
	peerID       string
	authority    string
	changed      chan struct{}
	committed    chan struct{}
}

type Receipt struct {
	ConfirmationScope string
	TransactionHash   crypto.Hash
	BlockHash         crypto.Hash
	BlockHeight       uint64
	TransactionIndex  uint64
	GasUsed           uint64
	Fee               uint64
}

type Status struct {
	Mode      string
	PeerID    string
	Authority string
	ChainID   uint64
	Height    uint64
	BlockHash crypto.Hash
	StateRoot crypto.Hash
	Pending   int
	Queued    int
}

type TransactionInfo struct {
	Transaction blockchain.Transaction
	Receipt     *Receipt // nil means currently in the mempool
}

type TransactionProof struct {
	Receipt Receipt
	Root    crypto.Hash
	Proof   crypto.MerkleProof
}

func Open(dir string, genesis blockchain.Genesis) (*Node, error) {
	return open(dir, genesis, nil, nil)
}

func OpenNetwork(dir string, genesis blockchain.Genesis, peerID, authority string) (*Node, error) {
	return open(dir, genesis, &storage.NetworkBinding{Version: 1, PeerID: peerID, Authority: authority}, nil)
}

func open(dir string, genesis blockchain.Genesis, binding *storage.NetworkBinding, validators *consensus.ValidatorSet) (*Node, error) {
	s, err := storage.Open(dir, genesis)
	if err != nil {
		return nil, err
	}
	if err := s.BindNetwork(binding); err != nil {
		_ = s.Close()
		return nil, err
	}
	chain, err := genesis.Chain()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	pool, err := mempool.New(genesis.ChainID, mempool.DefaultCapacity)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	n := &Node{chain: chain, pool: pool, store: s, receipts: make(map[crypto.Hash]Receipt), mode: "single-node", changed: make(chan struct{}), committed: make(chan struct{})}
	n.validators = validators
	n.certificates = make(map[uint64]consensus.Certificate)
	if binding != nil {
		n.peerID, n.authority = binding.PeerID, binding.Authority
		n.mode = "follower"
		if binding.PeerID == binding.Authority {
			n.mode = "authority"
		}
	}
	if validators != nil {
		n.mode = "bft-experimental"
	}
	blocks, certificates := s.Blocks(), s.Certificates()
	if (validators != nil && len(certificates) != len(blocks)) || (validators == nil && len(certificates) != 0) {
		_ = s.Close()
		return nil, fmt.Errorf("block/certificate history mismatch")
	}
	for i, block := range blocks {
		if validators != nil {
			if err := validators.VerifyCertificate(certificates[i], consensus.Precommit, block.Header.Height, block.Hash()); err != nil {
				_ = s.Close()
				return nil, fmt.Errorf("replay certificate %d: %w", block.Header.Height, err)
			}
			n.certificates[block.Header.Height] = certificates[i]
		}
		if err := chain.Append(block); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("replay block %d: %w", block.Header.Height, err)
		}
		n.index(block)
	}
	return n, nil
}

func (n *Node) check() error {
	if n.closed {
		return fmt.Errorf("node closed")
	}
	if n.fault != nil {
		return fmt.Errorf("node stopped after persistence failure; restart required: %w", n.fault)
	}
	return nil
}

func (n *Node) Submit(tx blockchain.Transaction) (crypto.Hash, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return crypto.Hash{}, err
	}
	if err := n.pool.Add(tx, n.chain.Snapshot()); err != nil {
		return crypto.Hash{}, err
	}
	n.notify(false)
	return tx.Hash(), nil
}

// Produce commits only nonempty executable blocks. If wall time has not moved
// beyond the tip, wait for the next tick instead of inventing future timestamps.
func (n *Node) Produce(now time.Time) (*blockchain.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if n.mode == "follower" || n.validators != nil {
		return nil, nil
	}
	tip, _ := n.chain.Header(math.MaxUint64)
	if now.Unix() <= tip.Timestamp {
		return nil, nil
	}
	txs := n.pool.Select(n.chain.Snapshot(), blockchain.MaxBlockTransactions)
	if len(txs) == 0 {
		return nil, nil
	}
	block, err := n.chain.BuildBlock(txs, now.Unix())
	if err != nil {
		return nil, err
	}
	if err := n.commit(block); err != nil {
		return nil, err
	}
	return &block, nil
}

func (n *Node) commit(block blockchain.Block) error {
	return n.commitUsing(block, n.store.Append)
}
func (n *Node) commitUsing(block blockchain.Block, appendBlock func(blockchain.Block) error) error {
	var diskError error
	err := n.chain.AppendWithCommit(block, func(b blockchain.Block) error { diskError = appendBlock(b); return diskError })
	if diskError != nil {
		n.fault = diskError
		n.notify(true)
	}
	if err != nil {
		return err
	}
	n.index(block)
	n.pool.Reconcile(n.chain.Snapshot())
	n.notify(true)
	return nil
}

// Import only accepts blocks obtained over an authenticated connection to the
// pinned authority. The P2P layer supplies the transport-authenticated peer ID.
func (n *Node) Import(block blockchain.Block, source string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return err
	}
	if n.mode != "follower" || source != n.authority {
		return fmt.Errorf("block source is not the configured authority")
	}
	if existing, ok := n.chain.Block(block.Header.Height); ok {
		if existing.Hash() == block.Hash() {
			return nil
		}
		return fmt.Errorf("conflict with committed block at height %d", block.Header.Height)
	}
	return n.commit(block)
}

func (n *Node) PendingPage(offset, limit int) ([]blockchain.Transaction, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	return n.pool.Page(offset, limit), nil
}

func (n *Node) Run(ctx context.Context, interval time.Duration) error {
	if interval < MinBlockInterval {
		return fmt.Errorf("block check interval must be at least %s", MinBlockInterval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if _, err := n.Produce(now); err != nil {
				return err
			}
		}
	}
}

func (n *Node) index(block blockchain.Block) {
	blockHash := block.Hash()
	scope := "local-durable"
	if n.validators != nil {
		scope = "certificate-verified-local"
	}
	for i, tx := range block.Transactions {
		fee, _ := tx.Fee()
		hash := tx.Hash()
		n.receipts[hash] = Receipt{ConfirmationScope: scope, TransactionHash: hash, BlockHash: blockHash, BlockHeight: block.Header.Height, TransactionIndex: uint64(i), GasUsed: blockchain.TransferGas, Fee: fee}
	}
}

func (n *Node) Status() (Status, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return Status{}, err
	}
	header, _ := n.chain.Header(math.MaxUint64)
	tip := blockchain.Block{Header: header}
	pending, queued := n.pool.Status(n.chain.Snapshot())
	return Status{Mode: n.mode, PeerID: n.peerID, Authority: n.authority, ChainID: tip.Header.ChainID, Height: tip.Header.Height, BlockHash: tip.Hash(), StateRoot: tip.Header.StateRoot, Pending: pending, Queued: queued}, nil
}

func (n *Node) Account(address string) (blockchain.Account, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return blockchain.Account{}, err
	}
	if !crypto.ValidAddress(address) {
		return blockchain.Account{}, fmt.Errorf("invalid address")
	}
	return n.chain.Account(address), nil
}

// math.MaxUint64 is the latest-block selector.
func (n *Node) Block(height uint64) (*blockchain.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if height == math.MaxUint64 {
		b := n.chain.Tip()
		return &b, nil
	}
	b, ok := n.chain.Block(height)
	if !ok {
		return nil, nil
	}
	return &b, nil
}

func (n *Node) Transaction(hash crypto.Hash) (*TransactionInfo, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if receipt, ok := n.receipts[hash]; ok {
		tx, _ := n.chain.TransactionAt(receipt.BlockHeight, receipt.TransactionIndex)
		return &TransactionInfo{Transaction: tx, Receipt: &receipt}, nil
	}
	if tx, ok := n.pool.Find(hash); ok {
		return &TransactionInfo{Transaction: tx}, nil
	}
	return nil, nil
}

func (n *Node) Proof(hash crypto.Hash) (*TransactionProof, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	r, ok := n.receipts[hash]
	if !ok {
		return nil, nil
	}
	block, _ := n.chain.Block(r.BlockHeight)
	proof, err := crypto.BuildProof(blockchain.TransactionLeaves(block.Transactions), int(r.TransactionIndex))
	if err != nil {
		return nil, err
	}
	return &TransactionProof{Receipt: r, Root: block.Header.TxRoot, Proof: proof}, nil
}

func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	n.notify(true)
	return n.store.Close()
}
