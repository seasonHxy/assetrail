package consensus

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"sort"

	"gochain/crypto"
)

// RoundNotice is an authenticated observation of a durably entered round. It
// cannot replace a vote, POL or commit certificate.
type RoundNotice struct {
	SetID     crypto.Hash `json:"setId"`
	Height    uint64      `json:"height,string"`
	Round     uint32      `json:"round,string"`
	Validator string      `json:"validator"`
	Signature []byte      `json:"signature"`
}

func roundBytes(n RoundNotice) []byte {
	var b bytes.Buffer
	b.WriteString("settlelab/consensus-round/v1")
	b.Write(n.SetID[:])
	_ = binary.Write(&b, binary.BigEndian, n.Height)
	_ = binary.Write(&b, binary.BigEndian, n.Round)
	b.WriteString(n.Validator)
	return b.Bytes()
}
func (s *ValidatorSet) VerifyRoundNotice(n RoundNotice) error {
	key, ok := s.keys[n.Validator]
	if !ok || n.SetID != s.id || n.Height == 0 || n.Round > MaxRound || !ed25519.Verify(key, roundBytes(n), n.Signature) {
		return fmt.Errorf("invalid round notice")
	}
	return nil
}
func (e *Engine) RoundNotice() (RoundNotice, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return RoundNotice{}, e.fault
	}
	n := RoundNotice{SetID: e.state.SetID, Height: e.state.Height, Round: e.state.Round, Validator: e.state.Validator}
	n.Signature = ed25519.Sign(e.key, roundBytes(n))
	return n, nil
}

// CatchUpRound requires f+1 distinct notices: a single faulty validator cannot
// move the pacemaker alone. Locks and valid certificates survive every jump.
func (e *Engine) CatchUpRound(notices []RoundNotice) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return e.fault
	}
	if len(notices) > 4 {
		return fmt.Errorf("too many round notices")
	}
	seen := map[string]bool{}
	rounds := []uint32{}
	for _, n := range notices {
		if err := e.set.VerifyRoundNotice(n); err != nil {
			return err
		}
		if n.Height != e.state.Height || seen[n.Validator] {
			return fmt.Errorf("duplicate or wrong-height notice")
		}
		seen[n.Validator] = true
		rounds = append(rounds, n.Round)
	}
	if len(rounds) < 2 || e.state.Decision != nil {
		return nil
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i] > rounds[j] })
	if rounds[1] <= e.state.Round {
		return nil
	}
	s := Clone(e.state)
	s.Round = rounds[1]
	s.Proposal = nil
	s.Prevote = nil
	s.Precommit = nil
	if err := e.save(s); err != nil {
		return err
	}
	e.votes = map[string]map[string]Vote{Prevote: {}, Precommit: {}}
	return nil
}

// NilVote is phase-specific and stale-safe. Persistence precedes gossip; an
// existing vote is never changed even when the deadline expires.
func (e *Engine) NilVote(height uint64, round uint32, phase string) (Actions, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return Actions{}, e.fault
	}
	if phase != Prevote && phase != Precommit {
		return Actions{}, fmt.Errorf("invalid timeout phase")
	}
	s := Clone(e.state)
	if s.Height != height || s.Round != round || s.Decision != nil {
		return Actions{}, nil
	}
	old := s.Prevote
	if phase == Precommit {
		old = s.Precommit
	}
	if old != nil {
		return e.progress()
	}
	v := e.signVote(&s, phase, crypto.Hash{})
	if err := e.save(s); err != nil {
		return Actions{}, err
	}
	e.votes[phase][s.Validator] = v
	a, err := e.progress()
	a.Votes = append([]Vote{Clone(v)}, a.Votes...)
	return a, err
}

// Gossip returns a coherent bounded snapshot, including relayed votes. Repeated
// exchange repairs dropped messages and restores volatile votes after a restart.
func (e *Engine) Gossip() (State, []Vote) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var votes []Vote
	for _, phase := range []string{Prevote, Precommit} {
		for _, id := range e.set.ids {
			if v, ok := e.votes[phase][id]; ok {
				votes = append(votes, Clone(v))
			}
		}
	}
	return Clone(e.state), votes
}
