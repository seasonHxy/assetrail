package consensus

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"sync"

	"gochain/blockchain"
	"gochain/crypto"
)

// Journal must atomically replace and fsync the record before returning success.
// A missing previously-created record is an error, never a vote-state reset.
type Journal interface {
	LoadConsensus() (*State, error)
	SaveConsensus(State) error
}

type State struct {
	Version   int         `json:"version"`
	SetID     crypto.Hash `json:"setId"`
	Validator string      `json:"validator"`
	Height    uint64      `json:"height,string"`
	Round     uint32      `json:"round,string"`
	Proposal  *Proposal   `json:"proposal,omitempty"`
	Prevote   *Vote       `json:"prevote,omitempty"`
	Precommit *Vote       `json:"precommit,omitempty"`
	Locked    *Decision   `json:"locked,omitempty"` // Certificate contains PREVOTEs.
	Valid     *Decision   `json:"valid,omitempty"`  // Highest observed current-round POL.
	Decision  *Decision   `json:"decision,omitempty"`
}

type Actions struct {
	Votes    []Vote    `json:"votes,omitempty"`
	Decision *Decision `json:"decision,omitempty"`
}

type Engine struct {
	mu       sync.Mutex
	set      *ValidatorSet
	key      ed25519.PrivateKey
	journal  Journal
	validate func(blockchain.Block) error
	state    State
	votes    map[string]map[string]Vote
	fault    error
}

func NewEngine(set *ValidatorSet, key ed25519.PrivateKey, height uint64, journal Journal, validate func(blockchain.Block) error) (*Engine, error) {
	if set == nil || len(key) != ed25519.PrivateKeySize || height == 0 || journal == nil || validate == nil {
		return nil, fmt.Errorf("missing engine configuration")
	}
	if !bytes.Equal(key, ed25519.NewKeyFromSeed(key.Seed())) {
		return nil, fmt.Errorf("inconsistent Ed25519 private key")
	}
	id := ValidatorID(key.Public().(ed25519.PublicKey))
	if !set.Contains(id) {
		return nil, fmt.Errorf("signer is not a validator")
	}
	e := &Engine{set: set, key: append(ed25519.PrivateKey(nil), key...), journal: journal, validate: validate, votes: map[string]map[string]Vote{Prevote: {}, Precommit: {}}}
	s, err := journal.LoadConsensus()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("missing safety journal")
	}
	if s.Version != 1 || s.SetID != set.ID() || s.Validator != id || s.Height == 0 || s.Round > MaxRound {
		return nil, fmt.Errorf("safety journal binding mismatch")
	}
	if s.Height != height {
		// The caller requests the next height only AFTER the certified block is
		// durably appended. Never discard an undecided height or roll back a round.
		if height < s.Height || height-s.Height != 1 || s.Decision == nil || s.Decision.Block.Header.Height != s.Height || set.VerifyDecision(*s.Decision) != nil {
			return nil, fmt.Errorf("cannot reset undecided or future safety journal")
		}
		e.state = *s
		next := State{Version: 1, SetID: set.ID(), Validator: id, Height: height}
		if err := e.save(next); err != nil {
			return nil, err
		}
	} else {
		e.state = Clone(*s)
		if err := e.validateState(); err != nil {
			return nil, err
		}
	}
	for _, v := range []*Vote{e.state.Prevote, e.state.Precommit} {
		if v != nil {
			e.votes[v.Phase][v.Validator] = Clone(*v)
		}
	}
	if d := e.state.Valid; d != nil && d.Certificate.Votes[0].Round == e.state.Round {
		for _, v := range d.Certificate.Votes {
			e.votes[Prevote][v.Validator] = Clone(v)
		}
	}
	return e, nil
}

func (e *Engine) validateState() error {
	s := e.state
	for _, v := range []*Vote{s.Prevote, s.Precommit} {
		if v == nil {
			continue
		}
		if e.set.VerifyVote(*v) != nil || v.Validator != s.Validator || v.Height != s.Height || v.Round != s.Round {
			return fmt.Errorf("invalid persisted local vote")
		}
	}
	if (s.Prevote != nil && s.Prevote.Phase != Prevote) || (s.Precommit != nil && s.Precommit.Phase != Precommit) {
		return fmt.Errorf("persisted phase mismatch")
	}
	for _, d := range []*Decision{s.Locked, s.Valid} {
		if d == nil {
			continue
		}
		if d.Block.Header.Height != s.Height || e.set.VerifyCertificate(d.Certificate, Prevote, s.Height, d.Block.Hash()) != nil || d.Certificate.Votes[0].Round > s.Round {
			return fmt.Errorf("invalid persisted lock/POL")
		}
		if err := e.validate(d.Block.Clone()); err != nil {
			return err
		}
	}
	if s.Locked != nil && (s.Valid == nil || s.Valid.Certificate.Votes[0].Round < s.Locked.Certificate.Votes[0].Round) {
		return fmt.Errorf("valid round behind lock")
	}
	if s.Precommit != nil && s.Precommit.BlockHash != (crypto.Hash{}) {
		if s.Locked == nil || s.Locked.Block.Hash() != s.Precommit.BlockHash || s.Locked.Certificate.Votes[0].Round != s.Round {
			return fmt.Errorf("non-nil precommit without matching lock")
		}
	}
	if s.Proposal != nil {
		if s.Proposal.Round != s.Round || s.Proposal.Block.Header.Height != s.Height || e.set.VerifyProposal(*s.Proposal) != nil {
			return fmt.Errorf("invalid persisted proposal")
		}
		if err := e.validate(s.Proposal.Block.Clone()); err != nil {
			return err
		}
	}
	if s.Decision != nil {
		if s.Decision.Block.Header.Height != s.Height || e.set.VerifyDecision(*s.Decision) != nil {
			return fmt.Errorf("invalid persisted decision")
		}
		if err := e.validate(s.Decision.Block.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) save(s State) error {
	if e.fault != nil {
		return e.fault
	}
	if err := e.journal.SaveConsensus(Clone(s)); err != nil {
		e.fault = fmt.Errorf("consensus persistence failure; reopen before signing: %w", err)
		return e.fault
	}
	e.state = Clone(s)
	return nil
}
func (e *Engine) State() State { e.mu.Lock(); defer e.mu.Unlock(); return Clone(e.state) }

func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fault = fmt.Errorf("consensus engine closed")
	for i := range e.key {
		e.key[i] = 0
	}
}

// Propose reuses the highest known valid block/POL, including across restarts.
func (e *Engine) Propose(candidate blockchain.Block) (*Proposal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return nil, e.fault
	}
	s := Clone(e.state)
	if s.Decision != nil {
		return nil, fmt.Errorf("height already decided")
	}
	if e.set.Proposer(s.Height, s.Round) != s.Validator {
		return nil, fmt.Errorf("not this round's proposer")
	}
	if s.Proposal != nil {
		return Clone(s.Proposal), nil
	}
	p := Proposal{SetID: e.set.ID(), Round: s.Round, Block: candidate.Clone(), Proposer: s.Validator}
	if s.Valid != nil {
		p.Block = s.Valid.Block.Clone()
		c := Clone(s.Valid.Certificate)
		p.POL = &c
	}
	if p.Block.Header.Height != s.Height {
		return nil, fmt.Errorf("proposal height mismatch")
	}
	if err := e.validate(p.Block.Clone()); err != nil {
		return nil, err
	}
	p.Signature = ed25519.Sign(e.key, proposalBytes(p))
	if err := e.set.VerifyProposal(p); err != nil {
		return nil, err
	}
	s.Proposal = &p
	if err := e.save(s); err != nil {
		return nil, err
	}
	return Clone(&p), nil
}

func (e *Engine) signVote(s *State, phase string, hash crypto.Hash) Vote {
	v := Vote{SetID: e.set.ID(), Height: s.Height, Round: s.Round, Phase: phase, BlockHash: hash, Validator: s.Validator}
	v.Signature = ed25519.Sign(e.key, voteBytes(v))
	if phase == Prevote {
		s.Prevote = &v
	} else {
		s.Precommit = &v
	}
	return v
}

func (e *Engine) ReceiveProposal(p Proposal) (Actions, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return Actions{}, e.fault
	}
	if e.state.Decision != nil {
		return Actions{Decision: Clone(e.state.Decision)}, nil
	}
	if err := e.set.VerifyProposal(p); err != nil {
		return Actions{}, err
	}
	s := Clone(e.state)
	if p.Round != s.Round || p.Block.Header.Height != s.Height {
		return Actions{}, fmt.Errorf("proposal is not at current height/round")
	}
	if err := e.validate(p.Block.Clone()); err != nil {
		return Actions{}, err
	}
	if s.Proposal != nil && !bytesEqualProposal(*s.Proposal, p) {
		return Actions{}, fmt.Errorf("conflicting proposal in the same round")
	}
	s.Proposal = Clone(&p)
	a := Actions{}
	if s.Prevote == nil {
		hash := p.Block.Hash()
		allowed := s.Locked == nil || s.Locked.Block.Hash() == hash
		if !allowed && p.POL != nil {
			allowed = p.POL.Votes[0].Round >= s.Locked.Certificate.Votes[0].Round
		}
		if !allowed {
			hash = crypto.Hash{}
		}
		v := e.signVote(&s, Prevote, hash)
		if err := e.save(s); err != nil {
			return Actions{}, err
		}
		e.votes[Prevote][v.Validator] = Clone(v)
		a.Votes = append(a.Votes, v)
	} else {
		if e.state.Proposal == nil {
			if err := e.save(s); err != nil {
				return Actions{}, err
			}
		}
		a.Votes = append(a.Votes, Clone(*s.Prevote))
	}
	next, err := e.progress()
	if err != nil {
		return Actions{}, err
	}
	a.Votes = append(a.Votes, next.Votes...)
	a.Decision = next.Decision
	return a, nil
}

func bytesEqualProposal(a, b Proposal) bool {
	return string(proposalBytes(a)) == string(proposalBytes(b))
}

func (e *Engine) ReceiveVote(v Vote) (Actions, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return Actions{}, e.fault
	}
	if err := e.set.VerifyVote(v); err != nil {
		return Actions{}, err
	}
	if v.Height != e.state.Height || v.Round != e.state.Round {
		return Actions{}, fmt.Errorf("vote is not at current height/round; retransmit current messages or a full certificate")
	}
	if old, ok := e.votes[v.Phase][v.Validator]; ok && old.BlockHash != v.BlockHash {
		return Actions{}, fmt.Errorf("equivocating vote")
	}
	e.votes[v.Phase][v.Validator] = Clone(v)
	return e.progress()
}

func (e *Engine) quorum(phase string, hash crypto.Hash) *Certificate {
	c := Certificate{}
	for _, id := range e.set.ids {
		if v, ok := e.votes[phase][id]; ok && v.BlockHash == hash {
			c.Votes = append(c.Votes, Clone(v))
		}
	}
	if len(c.Votes) < Quorum {
		return nil
	}
	return &c
}

func (e *Engine) progress() (Actions, error) {
	s := Clone(e.state)
	if s.Decision != nil {
		return Actions{Decision: Clone(s.Decision)}, nil
	}
	a := Actions{}
	if s.Proposal != nil {
		block := s.Proposal.Block
		if c := e.quorum(Precommit, block.Hash()); c != nil {
			d := Decision{Block: block.Clone(), Certificate: *c}
			s.Decision = &d
			if err := e.save(s); err != nil {
				return Actions{}, err
			}
			return Actions{Decision: Clone(&d)}, nil
		}
		if c := e.quorum(Prevote, block.Hash()); c != nil && s.Prevote != nil {
			d := Decision{Block: block.Clone(), Certificate: *c}
			changed := s.Valid == nil || s.Valid.Certificate.Votes[0].Round < s.Round
			if changed {
				s.Valid = &d
			}
			if s.Precommit == nil {
				s.Locked = &d
				v := e.signVote(&s, Precommit, block.Hash())
				a.Votes = append(a.Votes, v)
				changed = true
			}
			if changed {
				if err := e.save(s); err != nil {
					return Actions{}, err
				}
				if s.Precommit != nil {
					e.votes[Precommit][s.Validator] = Clone(*s.Precommit)
				}
			}
		}
	}
	if s.Precommit == nil && e.quorum(Prevote, crypto.Hash{}) != nil {
		v := e.signVote(&s, Precommit, crypto.Hash{})
		if err := e.save(s); err != nil {
			return Actions{}, err
		}
		e.votes[Precommit][s.Validator] = Clone(v)
		a.Votes = append(a.Votes, v)
	}
	if s.Proposal != nil {
		if c := e.quorum(Precommit, s.Proposal.Block.Hash()); c != nil {
			d := Decision{Block: s.Proposal.Block.Clone(), Certificate: *c}
			s.Decision = &d
			if err := e.save(s); err != nil {
				return Actions{}, err
			}
			a.Decision = Clone(&d)
		}
	}
	return a, nil
}

// Timeout advances a round while retaining locks/POL. Stale timer callbacks are
// ignored. A future transport must supply a pacemaker; this core has no wall clock.
func (e *Engine) Timeout(height uint64, round uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return e.fault
	}
	s := Clone(e.state)
	if s.Decision != nil || height != s.Height || round != s.Round {
		return nil
	}
	if s.Round == MaxRound {
		return fmt.Errorf("round limit reached; refusing to wrap")
	}
	s.Round++
	s.Proposal = nil
	s.Prevote = nil
	s.Precommit = nil
	if err := e.save(s); err != nil {
		return err
	}
	e.votes = map[string]map[string]Vote{Prevote: {}, Precommit: {}}
	return nil
}

// ReceiveDecision accepts a complete certificate from any round at this height,
// even when individual delayed votes are no longer retained by the core.
func (e *Engine) ReceiveDecision(d Decision) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return e.fault
	}
	if d.Block.Header.Height != e.state.Height {
		return fmt.Errorf("decision height mismatch")
	}
	if err := e.set.VerifyDecision(d); err != nil {
		return err
	}
	if err := e.validate(d.Block.Clone()); err != nil {
		return err
	}
	if e.state.Decision != nil {
		if e.state.Decision.Block.Hash() != d.Block.Hash() {
			return fmt.Errorf("conflicting decided block")
		}
		return nil
	}
	s := Clone(e.state)
	s.Decision = Clone(&d)
	return e.save(s)
}
