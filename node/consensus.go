package node

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"time"

	"gochain/blockchain"
	"gochain/consensus"
	"gochain/storage"
)

func OpenConsensus(dir string, g blockchain.Genesis, id string, set *consensus.ValidatorSet) (*Node, error) {
	chain, err := g.Chain()
	if err != nil {
		return nil, err
	}
	if set == nil || !set.Contains(id) || set.ChainID() != g.ChainID || set.Genesis() != chain.Tip().Hash() {
		return nil, fmt.Errorf("validator set does not match genesis or local signer")
	}
	return open(dir, g, &storage.NetworkBinding{Version: 2, PeerID: id, ConsensusHash: set.ID().String()}, set)
}

// NewConsensusEngine allows one active signing core per node. Call again after
// CommitCertified to start the next height. Old engine handles are disabled.
func (n *Node) NewConsensusEngine(key ed25519.PrivateKey) (*consensus.Engine, error) {
	n.engineMu.Lock()
	defer n.engineMu.Unlock()
	n.mu.Lock()
	err := n.check()
	set := n.validators
	header, _ := n.chain.Header(math.MaxUint64)
	n.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if set == nil || header.Height == math.MaxUint64 {
		return nil, fmt.Errorf("not a usable consensus node")
	}
	if n.engine != nil {
		return nil, fmt.Errorf("consensus engine already active at this height")
	}
	e, err := consensus.NewEngine(set, key, header.Height+1, n, n.ValidateProposal)
	if err == nil {
		n.engine = e
	}
	return e, err
}

func (n *Node) Candidate(now time.Time) (*blockchain.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if n.validators == nil {
		return nil, fmt.Errorf("not a consensus node")
	}
	tip, _ := n.chain.Header(math.MaxUint64)
	if now.Unix() <= tip.Timestamp {
		return nil, nil
	}
	txs := n.pool.Select(n.chain.Snapshot(), blockchain.MaxBlockTransactions)
	if len(txs) == 0 {
		return nil, nil
	}
	b, err := n.chain.BuildBlock(txs, now.Unix())
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (n *Node) ValidateProposal(b blockchain.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return err
	}
	if n.validators == nil || len(b.Transactions) == 0 {
		return fmt.Errorf("consensus requires a nonempty candidate")
	}
	return n.chain.Validate(b)
}

type consensusStore interface {
	consensus.Journal
	AppendCertified(blockchain.Block, consensus.Certificate) error
}

func (n *Node) LoadConsensus() (*consensus.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	s, ok := n.store.(consensusStore)
	if !ok || n.validators == nil {
		return nil, fmt.Errorf("no consensus journal")
	}
	return s.LoadConsensus()
}
func (n *Node) SaveConsensus(state consensus.State) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return err
	}
	s, ok := n.store.(consensusStore)
	if !ok || n.validators == nil {
		return fmt.Errorf("no consensus journal")
	}
	if state.SetID != n.validators.ID() || state.Validator != n.peerID {
		return fmt.Errorf("safety journal identity mismatch")
	}
	if err := s.SaveConsensus(state); err != nil {
		n.fault = err
		n.notify(true)
		return err
	}
	return nil
}

// CommitCertified is the only committing entry point in experimental BFT mode.
// Blocks and certificates share the same atomic snapshot; the safety journal is
// persisted first. A crash between the two leaves a recoverable decided height.
func (n *Node) CommitCertified(d consensus.Decision) error {
	n.engineMu.Lock()
	defer n.engineMu.Unlock()
	d = consensus.Clone(d)
	n.mu.Lock()
	if err := n.check(); err != nil {
		n.mu.Unlock()
		return err
	}
	if n.validators == nil {
		n.mu.Unlock()
		return fmt.Errorf("not a consensus node")
	}
	if err := n.validators.VerifyDecision(d); err != nil {
		n.mu.Unlock()
		return err
	}
	if b, ok := n.chain.Block(d.Block.Header.Height); ok {
		n.mu.Unlock()
		if b.Hash() != d.Block.Hash() {
			return fmt.Errorf("conflict with committed block")
		}
		return nil
	}
	if len(d.Block.Transactions) == 0 {
		n.mu.Unlock()
		return fmt.Errorf("empty consensus block")
	}
	if err := n.chain.Validate(d.Block); err != nil {
		n.mu.Unlock()
		return err
	}
	n.mu.Unlock()
	// Quiesce the signer before updating its journal or advancing the ledger.
	if n.engine != nil {
		n.engine.Close()
		n.engine = nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return err
	}
	s, ok := n.store.(consensusStore)
	if !ok {
		return fmt.Errorf("no certified store")
	}
	state, err := s.LoadConsensus()
	if err != nil {
		return err
	}
	if state.Height > d.Block.Header.Height {
		return fmt.Errorf("safety journal ahead of ledger")
	}
	if state.Height < d.Block.Header.Height {
		if d.Block.Header.Height-state.Height != 1 || state.Decision == nil || n.validators.VerifyDecision(*state.Decision) != nil {
			return fmt.Errorf("cannot skip undecided safety journal")
		}
		state = &consensus.State{Version: 1, SetID: n.validators.ID(), Validator: n.peerID, Height: d.Block.Header.Height}
	}
	if state.Decision != nil && state.Decision.Block.Hash() != d.Block.Hash() {
		return fmt.Errorf("conflicting safety decision")
	}
	state.Decision = &d
	if err := s.SaveConsensus(*state); err != nil {
		n.fault = err
		n.notify(true)
		return err
	}
	if err := n.commitUsing(d.Block, func(b blockchain.Block) error { return s.AppendCertified(b, d.Certificate) }); err != nil {
		return err
	}
	n.certificates[d.Block.Header.Height] = consensus.Clone(d.Certificate)
	return nil
}

func (n *Node) CommitCertificate(height uint64) (*consensus.Certificate, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.check(); err != nil {
		return nil, err
	}
	if height == math.MaxUint64 {
		tip, _ := n.chain.Header(height)
		height = tip.Height
	}
	c, ok := n.certificates[height]
	if !ok {
		return nil, nil
	}
	return consensus.Clone(&c), nil
}
