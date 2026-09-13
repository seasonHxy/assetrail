// Package bftdemo runs four real signing cores and durable node stores with a
// deterministic in-process message scheduler. It does not simulate TCP latency.
package bftdemo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gochain/blockchain"
	"gochain/consensus"
	"gochain/crypto"
	"gochain/node"
)

type Check struct {
	Name     string              `json:"name"`
	Passed   bool                `json:"passed"`
	Details  string              `json:"details"`
	Decision *consensus.Decision `json:"decision,omitempty"`
}
type Report struct {
	Started  string  `json:"startedAt"`
	Finished string  `json:"finishedAt"`
	Mode     string  `json:"mode"`
	Passed   bool    `json:"passed"`
	Checks   []Check `json:"checks"`
	Error    string  `json:"error,omitempty"`
}
type cluster struct {
	dir        string
	g          blockchain.Genesis
	set        *consensus.ValidatorSet
	keys       map[string]ed25519.PrivateKey
	nodes      map[string]*node.Node
	engines    map[string]*consensus.Engine
	wallet     *crypto.PrivateKey
	alice, bob string
}

func newCluster(dir string) (*cluster, error) {
	c := &cluster{dir: dir, keys: map[string]ed25519.PrivateKey{}, nodes: map[string]*node.Node{}, engines: map[string]*consensus.Engine{}}
	var err error
	c.wallet, err = crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			c.close()
		}
	}()
	c.alice, _ = crypto.Address(c.wallet.PubKey().SerializeCompressed())
	bob, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	defer bob.Zero()
	c.bob, _ = crypto.Address(bob.PubKey().SerializeCompressed())
	c.g = blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{c.alice: 1000}}
	chain, err := c.g.Chain()
	if err != nil {
		return nil, err
	}
	var pubs []ed25519.PublicKey
	for i := 0; i < 4; i++ {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		pubs = append(pubs, pub)
		c.keys[consensus.ValidatorID(pub)] = key
	}
	c.set, err = consensus.NewValidatorSet(c.g.ChainID, chain.Tip().Hash(), pubs)
	if err != nil {
		return nil, err
	}
	for _, id := range c.set.IDs() {
		if err := c.restart(id); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(filepath.Join(dir, "validators.json"), map[string]any{"chainId": "9001", "genesisHash": chain.Tip().Hash(), "setId": c.set.ID(), "validators": c.set.IDs(), "quorum": "3"}); err != nil {
		return nil, err
	}
	success = true
	return c, nil
}
func (c *cluster) stop(id string) error {
	if e := c.engines[id]; e != nil {
		e.Close()
		delete(c.engines, id)
	}
	if n := c.nodes[id]; n != nil {
		delete(c.nodes, id)
		return n.Close()
	}
	return nil
}
func (c *cluster) restart(id string) error {
	if err := c.stop(id); err != nil {
		return err
	}
	n, err := node.OpenConsensus(filepath.Join(c.dir, id), c.g, id, c.set)
	if err != nil {
		return err
	}
	c.nodes[id] = n
	e, err := n.NewConsensusEngine(c.keys[id])
	if err != nil {
		return err
	}
	c.engines[id] = e
	return nil
}
func (c *cluster) close() {
	for _, id := range c.setIDs() {
		_ = c.stop(id)
	}
	if c.wallet != nil {
		c.wallet.Zero()
	}
	for _, key := range c.keys {
		for i := range key {
			key[i] = 0
		}
	}
}
func (c *cluster) setIDs() []string {
	if c.set == nil {
		return nil
	}
	return c.set.IDs()
}
func (c *cluster) submit(nonce uint64) (blockchain.Transaction, error) {
	tx, err := (blockchain.Transaction{ChainID: 9001, To: c.bob, Amount: 20, Nonce: nonce, GasLimit: blockchain.TransferGas}).Sign(c.wallet)
	if err != nil {
		return tx, err
	}
	for _, n := range c.nodes {
		if _, err := n.Submit(tx); err != nil {
			return tx, err
		}
	}
	return tx, nil
}
func (c *cluster) proposal(height uint64, round uint32) (*consensus.Proposal, error) {
	id := c.set.Proposer(height, round)
	n := c.nodes[id]
	if n == nil {
		return nil, fmt.Errorf("proposer offline")
	}
	b, err := n.Candidate(time.Unix(int64(height), 0))
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("missing candidate")
	}
	return c.engines[id].Propose(*b)
}
func (c *cluster) deliver(proposals map[string]consensus.Proposal, active []string, dropPrecommits bool) (*consensus.Decision, error) {
	var queue []consensus.Vote
	var decided *consensus.Decision
	for _, id := range active {
		a, err := c.engines[id].ReceiveProposal(consensus.Clone(proposals[id]))
		if err != nil {
			return nil, err
		}
		queue = append(queue, a.Votes...)
	}
	for step := 0; len(queue) != 0; step++ {
		if step > 256 {
			return nil, fmt.Errorf("message budget exhausted")
		}
		v := queue[0]
		queue = queue[1:]
		if dropPrecommits && v.Phase == consensus.Precommit {
			continue
		}
		for _, id := range active {
			a, err := c.engines[id].ReceiveVote(consensus.Clone(v))
			if err != nil {
				return nil, err
			}
			queue = append(queue, a.Votes...)
			if a.Decision != nil {
				if decided != nil && decided.Block.Hash() != a.Decision.Block.Hash() {
					return nil, fmt.Errorf("conflicting decisions")
				}
				decided = a.Decision
			}
		}
	}
	return decided, nil
}
func broadcast(p *consensus.Proposal, ids []string) map[string]consensus.Proposal {
	m := map[string]consensus.Proposal{}
	for _, id := range ids {
		m[id] = consensus.Clone(*p)
	}
	return m
}
func (c *cluster) commit(d *consensus.Decision, ids []string) error {
	if d == nil {
		return fmt.Errorf("no decision")
	}
	for _, id := range ids {
		if err := c.nodes[id].CommitCertified(*d); err != nil {
			return err
		}
	}
	return c.checkLedger(ids, d.Block.Header.Height, d.Block.Hash())
}
func (c *cluster) checkLedger(ids []string, height uint64, hash crypto.Hash) error {
	for _, id := range ids {
		n := c.nodes[id]
		s, err := n.Status()
		if err != nil {
			return err
		}
		if s.Height != height || s.BlockHash != hash {
			return fmt.Errorf("ledger hash/height mismatch")
		}
		a, err := n.Account(c.alice)
		if err != nil {
			return err
		}
		b, err := n.Account(c.bob)
		if err != nil {
			return err
		}
		if a.Nonce != height || a.Balance != 1000-20*height || b.Balance != 20*height {
			return fmt.Errorf("balance or nonce mismatch")
		}
		cert, err := n.CommitCertificate(height)
		if err != nil || cert == nil {
			return fmt.Errorf("missing durable certificate: %v", err)
		}
		if err := c.set.VerifyCertificate(*cert, consensus.Precommit, height, hash); err != nil {
			return err
		}
	}
	return nil
}
func normal(c *cluster) (*consensus.Decision, error) {
	ids := c.set.IDs()
	var last *consensus.Decision
	for height := uint64(1); height <= 2; height++ {
		if _, err := c.submit(height - 1); err != nil {
			return nil, err
		}
		p, err := c.proposal(height, 0)
		if err != nil {
			return nil, err
		}
		d, err := c.deliver(broadcast(p, ids), ids, false)
		if err != nil {
			return nil, err
		}
		if err := c.commit(d, ids); err != nil {
			return nil, err
		}
		last = d
		for _, id := range ids {
			if err := c.restart(id); err != nil {
				return nil, err
			}
		}
		if err := c.checkLedger(ids, height, d.Block.Hash()); err != nil {
			return nil, err
		}
	}
	return last, nil
}
func offline(c *cluster, leader bool) (*consensus.Decision, error) {
	ids := c.set.IDs()
	active := ids[:3]
	missing := ids[3]
	round := uint32(0)
	if leader {
		active = ids[1:]
		missing = ids[0]
		round = 1
	}
	if err := c.stop(missing); err != nil {
		return nil, err
	}
	if _, err := c.submit(0); err != nil {
		return nil, err
	}
	if leader {
		for _, id := range active {
			if err := c.engines[id].Timeout(1, 0); err != nil {
				return nil, err
			}
		}
	}
	p, err := c.proposal(1, round)
	if err != nil {
		return nil, err
	}
	d, err := c.deliver(broadcast(p, active), active, false)
	if err != nil {
		return nil, err
	}
	if err := c.commit(d, active); err != nil {
		return nil, err
	}
	if err := c.restart(missing); err != nil {
		return nil, err
	}
	if err := c.commit(d, []string{missing}); err != nil {
		return nil, err
	}
	return d, c.checkLedger(ids, 1, d.Block.Hash())
}
func insufficient(c *cluster) (*consensus.Decision, error) {
	ids := c.set.IDs()
	active := ids[:2]
	for _, id := range ids[2:] {
		if err := c.stop(id); err != nil {
			return nil, err
		}
	}
	tx, err := c.submit(0)
	if err != nil {
		return nil, err
	}
	for round := uint32(0); round < 4; round++ {
		if c.nodes[c.set.Proposer(1, round)] != nil {
			p, err := c.proposal(1, round)
			if err != nil {
				return nil, err
			}
			d, err := c.deliver(broadcast(p, active), active, false)
			if err != nil {
				return nil, err
			}
			if d != nil {
				return nil, fmt.Errorf("two validators committed")
			}
		}
		for _, id := range active {
			s, err := c.nodes[id].Status()
			if err != nil || s.Height != 0 {
				return nil, fmt.Errorf("false committed height")
			}
			r, err := c.nodes[id].Receipt(tx.Hash())
			if err != nil || r != nil {
				return nil, fmt.Errorf("false receipt")
			}
			if err := c.engines[id].Timeout(1, round); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}
func lockRestart(c *cluster) (*consensus.Decision, error) {
	ids := c.set.IDs()
	if _, err := c.submit(0); err != nil {
		return nil, err
	}
	p, err := c.proposal(1, 0)
	if err != nil {
		return nil, err
	}
	d, err := c.deliver(broadcast(p, ids), ids, true)
	if err != nil {
		return nil, err
	}
	if d != nil {
		return nil, fmt.Errorf("committed without delivered precommits")
	}
	for _, id := range ids {
		if c.engines[id].State().Locked == nil {
			return nil, fmt.Errorf("lock not established")
		}
		if err := c.restart(id); err != nil {
			return nil, err
		}
		if c.engines[id].State().Locked == nil {
			return nil, fmt.Errorf("restart lost lock")
		}
		if err := c.engines[id].Timeout(1, 0); err != nil {
			return nil, err
		}
	}
	// Mempools are empty after restart. A persisted valid block/POL alone must
	// suffice for safe reproposal; the supplied alternative must not replace it.
	other := p.Block.Clone()
	other.Header.Timestamp = 2
	q, err := c.engines[c.set.Proposer(1, 1)].Propose(other)
	if err != nil {
		return nil, err
	}
	if q.POL == nil || q.Block.Hash() != p.Block.Hash() {
		return nil, fmt.Errorf("locked block replaced")
	}
	d, err = c.deliver(broadcast(q, ids), ids, false)
	if err != nil {
		return nil, err
	}
	return d, c.commit(d, ids)
}
func equivocation(c *cluster) (*consensus.Decision, error) {
	ids := c.set.IDs()
	if _, err := c.submit(0); err != nil {
		return nil, err
	}
	p, err := c.proposal(1, 0)
	if err != nil {
		return nil, err
	}
	// One malicious proposer deliberately clones its own key into a second
	// journal. Honest nodes keep their original keys/stores and see split proposals.
	fork, err := node.OpenConsensus(filepath.Join(c.dir, "malicious-fork"), c.g, ids[0], c.set)
	if err != nil {
		return nil, err
	}
	defer fork.Close()
	e, err := fork.NewConsensusEngine(c.keys[ids[0]])
	if err != nil {
		return nil, err
	}
	other := p.Block.Clone()
	other.Header.Timestamp = 2
	q, err := e.Propose(other)
	if err != nil {
		return nil, err
	}
	proposals := broadcast(p, ids)
	proposals[ids[2]] = *q
	proposals[ids[3]] = *q
	d, err := c.deliver(proposals, ids, false)
	if err != nil {
		return nil, err
	}
	if d != nil {
		return nil, fmt.Errorf("split votes falsely committed")
	}
	if _, err := c.engines[ids[1]].ReceiveProposal(*q); err == nil {
		return nil, fmt.Errorf("conflicting proposer message accepted")
	}
	if err := c.stop(ids[0]); err != nil {
		return nil, err
	}
	active := ids[1:]
	for _, id := range active {
		if err := c.engines[id].Timeout(1, 0); err != nil {
			return nil, err
		}
	}
	p, err = c.proposal(1, 1)
	if err != nil {
		return nil, err
	}
	d, err = c.deliver(broadcast(p, active), active, false)
	if err != nil {
		return nil, err
	}
	return d, c.commit(d, active)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func Run(dir string) (Report, error) {
	r := Report{Started: time.Now().UTC().Format(time.RFC3339Nano), Mode: "four signing engines; in-process message scheduler; real fsync stores; logical timeout injection"}
	cases := []struct {
		name, details string
		run           func(*cluster) (*consensus.Decision, error)
	}{
		{"normal-and-restart", "4 validators commit two heights; all journals and certificates survive reopen", normal},
		{"one-validator-offline", "3 validators commit; reopened missing validator imports the verified certificate", func(c *cluster) (*consensus.Decision, error) { return offline(c, false) }},
		{"proposer-offline", "logical timeout rotates to round 1; remaining 3 validators commit", func(c *cluster) (*consensus.Decision, error) { return offline(c, true) }},
		{"two-validators-offline", "4 logical rounds with only 2 validators; no block or receipt", insufficient},
		{"lock-restart-round-change", "drop precommits, persist locks, restart all, repropose certified locked block in round 1", lockRestart},
		{"equivocating-proposer", "signed split proposals cannot commit; 3 honest validators recover in round 1", equivocation},
	}
	var failure error
	for _, tc := range cases {
		c, err := newCluster(filepath.Join(dir, tc.name))
		var d *consensus.Decision
		if err == nil {
			d, err = tc.run(c)
			c.close()
		}
		r.Checks = append(r.Checks, Check{Name: tc.name, Passed: err == nil, Details: tc.details, Decision: d})
		if err != nil {
			failure = fmt.Errorf("%s: %w", tc.name, err)
			break
		}
	}
	r.Passed = failure == nil
	r.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	if failure != nil {
		r.Error = failure.Error()
	}
	if err := writeJSON(filepath.Join(dir, "report.json"), r); err != nil {
		return r, err
	}
	return r, failure
}
