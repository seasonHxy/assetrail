package bft

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"gochain/blockchain"
	"gochain/consensus"
	"gochain/node"
)

type driver struct {
	mu          sync.Mutex
	n           *node.Node
	set         *consensus.ValidatorSet
	key         ed25519.PrivateKey
	e           *consensus.Engine
	base        time.Duration
	deadline    time.Time
	timerHeight uint64
	timerRound  uint32
	timerPhase  string
	notices     map[string]consensus.RoundNotice
	timeouts    uint64
	catchups    uint64
	rejected    uint64
	fault       error
}
type ConsensusStatus struct {
	Validator string    `json:"validator"`
	SetID     string    `json:"setId"`
	Height    uint64    `json:"height,string"`
	Round     uint32    `json:"round,string"`
	Phase     string    `json:"phase"`
	Proposer  string    `json:"proposer"`
	Quorum    int       `json:"quorum"`
	Deadline  time.Time `json:"deadline"`
	Timeouts  uint64    `json:"timeouts,string"`
	Catchups  uint64    `json:"roundCatchups,string"`
	Rejected  uint64    `json:"rejectedMessages,string"`
	Fault     string    `json:"fault,omitempty"`
}

func newDriver(n *node.Node, set *consensus.ValidatorSet, key ed25519.PrivateKey, base time.Duration) (*driver, error) {
	if base < 500*time.Millisecond || base > 10*time.Second {
		return nil, fmt.Errorf("consensus timeout must be 500ms..10s")
	}
	d := &driver{n: n, set: set, key: append(ed25519.PrivateKey(nil), key...), base: base, notices: map[string]consensus.RoundNotice{}}
	// Recover a decision fsynced immediately before a crash interrupted the ledger append.
	saved, err := n.LoadConsensus()
	if err != nil {
		return nil, err
	}
	if saved.SetID != set.ID() {
		return nil, fmt.Errorf("driver validator set does not match node")
	}
	if saved.Decision != nil {
		if err = n.CommitCertified(*saved.Decision); err != nil {
			return nil, err
		}
	}
	d.e, err = n.NewConsensusEngine(d.key)
	if err != nil {
		return nil, err
	}
	d.resetTimer(time.Now())
	return d, nil
}
func phase(s consensus.State) string {
	if s.Precommit != nil {
		return consensus.Precommit
	}
	if s.Prevote != nil {
		return consensus.Prevote
	}
	return "propose"
}
func (d *driver) resetTimer(now time.Time) {
	s := d.e.State()
	p := phase(s)
	if s.Height != d.timerHeight || s.Round != d.timerRound || p != d.timerPhase {
		d.timerHeight = s.Height
		d.timerRound = s.Round
		d.timerPhase = p
		// Increasing deadlines permit eventual synchrony; arithmetic is bounded by MaxRound.
		d.deadline = now.Add(d.base + time.Duration(s.Round)*(d.base/2))
	}
}
func (d *driver) commit(dec consensus.Decision) error {
	if err := d.n.CommitCertified(dec); err != nil {
		return err
	}
	next, err := d.n.NewConsensusEngine(d.key)
	if err != nil {
		d.fault = err
		return err
	}
	d.e = next
	d.notices = map[string]consensus.RoundNotice{}
	d.resetTimer(time.Now())
	return nil
}
func (d *driver) finish() error {
	if s := d.e.State(); s.Decision != nil {
		return d.commit(*s.Decision)
	}
	d.resetTimer(time.Now())
	return nil
}
func (d *driver) tick(now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fault != nil {
		return d.fault
	}
	if _, err := d.n.Status(); err != nil {
		return err
	}
	if err := d.finish(); err != nil {
		return err
	}
	s := d.e.State()
	if d.set.Proposer(s.Height, s.Round) == s.Validator && s.Proposal == nil && s.Prevote == nil {
		var candidate *blockchain.Block
		var err error
		if s.Valid != nil {
			b := s.Valid.Block.Clone()
			candidate = &b
		} else {
			candidate, err = d.n.Candidate(now)
		}
		if err != nil {
			return err
		}
		if candidate != nil {
			p, err := d.e.Propose(*candidate)
			if err != nil {
				return err
			}
			if _, err = d.e.ReceiveProposal(*p); err != nil {
				return err
			}
			if err = d.finish(); err != nil {
				return err
			}
		}
	}
	d.resetTimer(now)
	if !now.Before(d.deadline) {
		s = d.e.State()
		var err error
		switch phase(s) {
		case "propose":
			_, err = d.e.NilVote(s.Height, s.Round, consensus.Prevote)
		case consensus.Prevote:
			_, err = d.e.NilVote(s.Height, s.Round, consensus.Precommit)
		case consensus.Precommit:
			err = d.e.Timeout(s.Height, s.Round)
		}
		if err != nil {
			return err
		}
		d.timeouts++
		return d.finish()
	}
	return nil
}
func (d *driver) status() ConsensusStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.e.State()
	result := ConsensusStatus{Validator: s.Validator, SetID: s.SetID.String(), Height: s.Height, Round: s.Round, Phase: phase(s), Proposer: d.set.Proposer(s.Height, s.Round), Quorum: consensus.Quorum, Deadline: d.deadline, Timeouts: d.timeouts, Catchups: d.catchups, Rejected: d.rejected}
	if d.fault != nil {
		result.Fault = d.fault.Error()
	}
	return result
}
func (d *driver) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.e != nil {
		d.e.Close()
	}
	for i := range d.key {
		d.key[i] = 0
	}
}
