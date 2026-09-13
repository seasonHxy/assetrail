// Package mempool maintains bounded per-account nonce queues.
package mempool

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"gochain/blockchain"
	"gochain/crypto"
)

const (
	DefaultCapacity        = 4096
	MaxNonceGap     uint64 = 64
)

type Pool struct {
	mu       sync.Mutex
	chainID  uint64
	capacity int
	size     int
	accounts map[string]map[uint64]blockchain.Transaction
	byHash   map[crypto.Hash]blockchain.Transaction
}

func (p *Pool) Find(hash crypto.Hash) (blockchain.Transaction, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tx, ok := p.byHash[hash]
	return tx.Clone(), ok
}

// NextNonce finds the first gap; it is a suggestion, not a reservation.
func (p *Pool) NextNonce(address string, confirmed uint64) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for nonce := confirmed; ; nonce++ {
		if nonce == math.MaxUint64 {
			return 0, fmt.Errorf("nonce exhausted")
		}
		if _, exists := p.accounts[address][nonce]; !exists {
			return nonce, nil
		}
	}
}

func New(chainID uint64, capacity int) (*Pool, error) {
	if chainID == 0 || capacity <= 0 {
		return nil, fmt.Errorf("invalid pool configuration")
	}
	return &Pool{chainID: chainID, capacity: capacity, accounts: make(map[string]map[uint64]blockchain.Transaction), byHash: make(map[crypto.Hash]blockchain.Transaction)}, nil
}

func (p *Pool) Add(tx blockchain.Transaction, state *blockchain.State) error {
	if state == nil {
		return fmt.Errorf("missing state snapshot")
	}
	tx = tx.Clone()
	if err := tx.Validate(p.chainID); err != nil {
		return err
	}
	nonce := state.Account(tx.From).Nonce
	if tx.Nonce < nonce {
		return fmt.Errorf("stale nonce")
	}
	if tx.Nonce-nonce > MaxNonceGap {
		return fmt.Errorf("nonce too far in future")
	}
	// Admission does not reserve balances. Selection simulates execution, so
	// pending incoming transfers can fund a transaction in the same block.
	p.mu.Lock()
	defer p.mu.Unlock()
	queue := p.accounts[tx.From]
	if old, ok := queue[tx.Nonce]; ok {
		if old.Hash() == tx.Hash() {
			return fmt.Errorf("duplicate transaction")
		}
		bump := old.GasPrice / 10
		if old.GasPrice%10 != 0 {
			bump++
		}
		if bump == 0 {
			bump = 1
		}
		if old.GasPrice > math.MaxUint64-bump || tx.GasPrice < old.GasPrice+bump {
			return fmt.Errorf("replacement requires at least 10%% price bump (minimum 1)")
		}
		delete(p.byHash, old.Hash())
	} else {
		if p.size >= p.capacity {
			return fmt.Errorf("mempool full")
		}
		p.size++
	}
	if queue == nil {
		queue = make(map[uint64]blockchain.Transaction)
		p.accounts[tx.From] = queue
	}
	queue[tx.Nonce] = tx
	p.byHash[tx.Hash()] = tx
	return nil
}

func (p *Pool) Len() int { p.mu.Lock(); defer p.mu.Unlock(); return p.size }

// Page provides deterministic bounded inventory for gossip and reconnect repair.
func (p *Pool) Page(offset, limit int) []blockchain.Transaction {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.size == 0 || limit <= 0 || offset < 0 {
		return nil
	}
	all := make([]blockchain.Transaction, 0, p.size)
	for _, queue := range p.accounts {
		for _, tx := range queue {
			all = append(all, tx)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].From != all[j].From {
			return all[i].From < all[j].From
		}
		return all[i].Nonce < all[j].Nonce
	})
	start := offset % len(all)
	if limit > len(all)-start {
		limit = len(all) - start
	}
	page := make([]blockchain.Transaction, limit)
	for i := range page {
		page[i] = all[start+i].Clone()
	}
	return page
}

// Status counts contiguous nonces as pending, irrespective of affordability.
// Queued transactions have a nonce gap. Select returns only executable entries.
func (p *Pool) Status(state *blockchain.State) (pending, queued int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for address, queue := range p.accounts {
		nonce := state.Account(address).Nonce
		for {
			if _, ok := queue[nonce]; !ok {
				break
			}
			pending++
			nonce++
		}
		for n := range queue {
			if n >= nonce {
				queued++
			}
		}
	}
	return pending, queued
}

// Select simulates one candidate per account at a time. Higher gas prices win;
// equal-price candidates are ordered by hash. A blocked sender is reconsidered
// after other senders execute. Selection never removes entries from the pool.
func (p *Pool) Select(state *blockchain.State, limit int) []blockchain.Transaction {
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit <= 0 {
		return nil
	}
	if limit > blockchain.MaxBlockTransactions {
		limit = blockchain.MaxBlockTransactions
	}
	working := state.Clone()
	var selected []blockchain.Transaction
	for len(selected) < limit {
		var candidates []blockchain.Transaction
		for address, queue := range p.accounts {
			if tx, ok := queue[working.Account(address).Nonce]; ok {
				candidates = append(candidates, tx)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].GasPrice != candidates[j].GasPrice {
				return candidates[i].GasPrice > candidates[j].GasPrice
			}
			return candidates[i].Hash().String() < candidates[j].Hash().String()
		})
		progress := false
		for _, tx := range candidates {
			if err := working.Apply(tx, p.chainID); err == nil {
				selected = append(selected, tx.Clone())
				progress = true
				break // Rebuild heads so newly available nonces compete on price.
			}
		}
		if !progress {
			break
		}
	}
	return selected
}

// Reconcile removes consumed nonces only after a block has been committed.
func (p *Pool) Reconcile(state *blockchain.State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for address, queue := range p.accounts {
		nonce := state.Account(address).Nonce
		for n := range queue {
			if n < nonce {
				delete(p.byHash, queue[n].Hash())
				delete(queue, n)
				p.size--
			}
		}
		if len(queue) == 0 {
			delete(p.accounts, address)
		}
	}
}
