package consensus

import (
	"gochain/crypto"
	"testing"
)

func TestNilTimeoutCannotOverwriteVoteAndSurvivesRestart(t *testing.T) {
	f := setup(t)
	id := f.set.IDs()[0]
	e := f.engines[id]
	a, err := e.NilVote(1, 0, Prevote)
	if err != nil || len(a.Votes) != 1 || a.Votes[0].BlockHash != (crypto.Hash{}) {
		t.Fatalf("nil prevote: %+v %v", a, err)
	}
	p := f.proposal(0, f.block, nil)
	if _, err = e.ReceiveProposal(p); err != nil {
		t.Fatal(err)
	}
	if e.State().Prevote.BlockHash != (crypto.Hash{}) {
		t.Fatal("late proposal overwrote nil vote")
	}
	e.Close()
	e, err = NewEngine(f.set, f.keys[id], 1, f.journals[id], f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.NilVote(1, 0, Precommit); err != nil {
		t.Fatal(err)
	}
	state, votes := e.Gossip()
	if state.Precommit == nil || state.Precommit.BlockHash != (crypto.Hash{}) || len(votes) != 2 {
		t.Fatal("restart did not preserve nil phases")
	}
	before := Clone(state)
	if _, err = e.NilVote(1, 1, Prevote); err != nil {
		t.Fatal(err)
	}
	if e.State().Round != before.Round {
		t.Fatal("stale timeout changed round")
	}
	if err = e.Timeout(1, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Timeout(1, 0); err != nil || e.State().Round != 1 {
		t.Fatal("stale callback advanced twice")
	}
}
func TestRoundCatchupRequiresDistinctAuthenticatedMembers(t *testing.T) {
	f := setup(t)
	ids := f.set.IDs()
	target := f.engines[ids[0]]
	notices := []RoundNotice{}
	for _, id := range ids[1:3] {
		for i := uint32(0); i < 4; i++ {
			if err := f.engines[id].Timeout(1, i); err != nil {
				t.Fatal(err)
			}
		}
		n, err := f.engines[id].RoundNotice()
		if err != nil {
			t.Fatal(err)
		}
		notices = append(notices, n)
	}
	if err := target.CatchUpRound(notices[:1]); err != nil || target.State().Round != 0 {
		t.Fatal("one member forced catch-up")
	}
	if err := target.CatchUpRound([]RoundNotice{notices[0], notices[0]}); err == nil {
		t.Fatal("duplicate accepted")
	}
	bad := Clone(notices)
	bad[1].Signature[0] ^= 1
	if err := target.CatchUpRound(bad); err == nil || target.State().Round != 0 {
		t.Fatal("forged notice advanced round")
	}
	bad = Clone(notices)
	bad[0].Height++
	if err := target.CatchUpRound(bad); err == nil {
		t.Fatal("height replay accepted")
	}
	if err := target.CatchUpRound(notices); err != nil || target.State().Round != 4 {
		t.Fatalf("catchup failed: %v", err)
	}
	restored, err := NewEngine(f.set, f.keys[ids[0]], 1, f.journals[ids[0]], f.chain.Validate)
	if err != nil || restored.State().Round != 4 {
		t.Fatal("catchup not durable", err)
	}
}
func TestTimeoutPersistenceFailureReleasesNoSignature(t *testing.T) {
	f := setup(t)
	id := f.set.IDs()[0]
	f.journals[id].fail = true
	a, err := f.engines[id].NilVote(1, 0, Prevote)
	if err == nil || len(a.Votes) != 0 {
		t.Fatal("unpersisted vote released")
	}
	if _, err = f.engines[id].RoundNotice(); err == nil {
		t.Fatal("failed journal still signing")
	}
}
func TestRoundCatchupRetainsLockAndValidProof(t *testing.T) {
	f := setup(t)
	ids := f.set.IDs()
	p := f.proposal(0, f.block, nil)
	var votes []Vote
	for _, id := range ids[:3] {
		a, err := f.engines[id].ReceiveProposal(p)
		if err != nil {
			t.Fatal(err)
		}
		votes = append(votes, a.Votes[0])
	}
	e := f.engines[ids[0]]
	for _, v := range votes {
		if _, err := e.ReceiveVote(v); err != nil {
			t.Fatal(err)
		}
	}
	if e.State().Locked == nil {
		t.Fatal("fixture not locked")
	}
	var notices []RoundNotice
	for _, id := range ids[1:3] {
		if err := f.engines[id].Timeout(1, 0); err != nil {
			t.Fatal(err)
		}
		n, err := f.engines[id].RoundNotice()
		if err != nil {
			t.Fatal(err)
		}
		notices = append(notices, n)
	}
	if err := e.CatchUpRound(notices); err != nil {
		t.Fatal(err)
	}
	s := e.State()
	if s.Round != 1 || s.Locked == nil || s.Valid == nil || s.Locked.Block.Hash() != f.block.Hash() || s.Prevote != nil || s.Precommit != nil {
		t.Fatal("catchup lost safety state")
	}
}
