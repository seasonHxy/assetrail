package consensus

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"gochain/blockchain"
	"gochain/crypto"
)

type memoryJournal struct {
	state State
	fail  bool
}

func TestConcurrentDuplicateDelivery(t *testing.T) {
	f := setup(t)
	ids := f.set.IDs()
	p := f.proposal(0, f.block, nil)
	var votes []Vote
	for _, id := range ids {
		a, err := f.engines[id].ReceiveProposal(p)
		if err != nil {
			t.Fatal(err)
		}
		votes = append(votes, a.Votes...)
	}
	precommits := make(chan Vote, 16)
	var wg sync.WaitGroup
	for _, v := range votes {
		for _, id := range ids {
			for copy := 0; copy < 2; copy++ {
				wg.Add(1)
				go func(id string, v Vote) {
					defer wg.Done()
					a, err := f.engines[id].ReceiveVote(v)
					if err != nil {
						t.Error(err)
						return
					}
					for _, v := range a.Votes {
						precommits <- v
					}
				}(id, v)
			}
		}
	}
	wg.Wait()
	close(precommits)
	for v := range precommits {
		for _, id := range ids {
			wg.Add(1)
			go func(id string, v Vote) {
				defer wg.Done()
				if _, err := f.engines[id].ReceiveVote(v); err != nil {
					t.Error(err)
				}
			}(id, v)
		}
	}
	wg.Wait()
	for _, e := range f.engines {
		d := e.State().Decision
		if d == nil || d.Block.Hash() != f.block.Hash() || f.set.VerifyDecision(*d) != nil {
			t.Fatal("concurrent delivery failed")
		}
	}
}

func (m *memoryJournal) LoadConsensus() (*State, error) { return Clone(&m.state), nil }
func (m *memoryJournal) SaveConsensus(s State) error {
	if m.fail {
		return errors.New("disk failure")
	}
	if err := ValidateTransition(m.state, s); err != nil {
		return err
	}
	m.state = Clone(s)
	return nil
}

func TestStaleEngineCannotOverwriteSignedJournal(t *testing.T) {
	f := setup(t)
	id := f.set.Proposer(1, 0)
	stale, err := NewEngine(f.set, f.keys[id], 1, f.journals[id], f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.engines[id].Propose(f.block); err != nil {
		t.Fatal(err)
	}
	other := f.block.Clone()
	other.Header.Timestamp = 2
	if p, err := stale.Propose(other); err == nil || p != nil {
		t.Fatal("stale signer overwrote durable proposal")
	}
	if f.journals[id].state.Proposal.Block.Hash() != f.block.Hash() {
		t.Fatal("journal changed after conflict")
	}
}

type fixture struct {
	set      *ValidatorSet
	keys     map[string]ed25519.PrivateKey
	engines  map[string]*Engine
	journals map[string]*memoryJournal
	chain    *blockchain.Chain
	block    blockchain.Block
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{keys: map[string]ed25519.PrivateKey{}, engines: map[string]*Engine{}, journals: map[string]*memoryJournal{}}
	var pubs []ed25519.PublicKey
	for i := 0; i < 4; i++ {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pubs = append(pubs, pub)
		f.keys[ValidatorID(pub)] = key
	}
	wallet, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer wallet.Zero()
	address, _ := crypto.Address(wallet.PubKey().SerializeCompressed())
	f.chain, err = blockchain.NewChain(9001, map[string]uint64{address: 1000})
	if err != nil {
		t.Fatal(err)
	}
	f.set, err = NewValidatorSet(9001, f.chain.Tip().Hash(), pubs)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := (blockchain.Transaction{ChainID: 9001, To: address, Amount: 1, GasLimit: blockchain.TransferGas}).Sign(wallet)
	if err != nil {
		t.Fatal(err)
	}
	f.block, err = f.chain.BuildBlock([]blockchain.Transaction{tx}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range f.set.IDs() {
		j := &memoryJournal{state: State{Version: 1, SetID: f.set.ID(), Validator: id, Height: 1}}
		f.journals[id] = j
		f.engines[id], err = NewEngine(f.set, f.keys[id], 1, j, f.chain.Validate)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *fixture) vote(id, phase string, round uint32, hash crypto.Hash) Vote {
	v := Vote{SetID: f.set.ID(), Height: 1, Round: round, Phase: phase, BlockHash: hash, Validator: id}
	v.Signature = ed25519.Sign(f.keys[id], voteBytes(v))
	return v
}
func (f *fixture) certificate(phase string, round uint32, b blockchain.Block, ids []string) Certificate {
	c := Certificate{}
	for _, id := range ids {
		c.Votes = append(c.Votes, f.vote(id, phase, round, b.Hash()))
	}
	return c
}
func (f *fixture) proposal(round uint32, b blockchain.Block, pol *Certificate) Proposal {
	p := Proposal{SetID: f.set.ID(), Round: round, Block: b, Proposer: f.set.Proposer(1, round), POL: pol}
	p.Signature = ed25519.Sign(f.keys[p.Proposer], proposalBytes(p))
	return p
}
func deliver(t *testing.T, f *fixture, p Proposal, active []string, reverse bool) *Decision {
	t.Helper()
	var queue []Vote
	var decision *Decision
	for _, id := range active {
		a, err := f.engines[id].ReceiveProposal(p)
		if err != nil {
			t.Fatal(err)
		}
		queue = append(queue, a.Votes...)
	}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("message loop")
		}
		index := 0
		if reverse {
			index = len(queue) - 1
		}
		v := queue[index]
		queue = append(queue[:index], queue[index+1:]...)
		for _, id := range active {
			a, err := f.engines[id].ReceiveVote(v)
			if err != nil {
				t.Fatal(err)
			}
			queue = append(queue, a.Votes...)
			if a.Decision != nil {
				if decision != nil && decision.Block.Hash() != a.Decision.Block.Hash() {
					t.Fatal("conflicting decisions")
				}
				decision = a.Decision
			}
		}
	}
	return decision
}

func TestQuorumOfflineAndMessageOrder(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		for _, reverse := range []bool{false, true} {
			f := setup(t)
			p, err := f.engines[f.set.Proposer(1, 0)].Propose(f.block)
			if err != nil {
				t.Fatal(err)
			}
			d := deliver(t, f, *p, f.set.IDs()[:count], reverse)
			if (d != nil) != (count >= 3) {
				t.Fatalf("%d active: decision=%v", count, d != nil)
			}
			if d != nil && f.set.VerifyDecision(*d) != nil {
				t.Fatal("bad decision certificate")
			}
		}
	}
}

func TestTimeoutChangesProposerAndStaleTimersIgnored(t *testing.T) {
	f := setup(t)
	active := f.set.IDs()[1:]
	for _, id := range active {
		e := f.engines[id]
		if err := e.Timeout(1, 0); err != nil {
			t.Fatal(err)
		}
		if err := e.Timeout(1, 0); err != nil {
			t.Fatal(err)
		}
		if e.State().Round != 1 {
			t.Fatal("stale timer changed round")
		}
	}
	p, err := f.engines[f.set.Proposer(1, 1)].Propose(f.block)
	if err != nil {
		t.Fatal(err)
	}
	if d := deliver(t, f, *p, active, false); d == nil {
		t.Fatal("leader replacement did not progress")
	}
}

func TestVoteAndCertificateAdversarialChecks(t *testing.T) {
	f := setup(t)
	ids := f.set.IDs()
	v := f.vote(ids[0], Prevote, 0, f.block.Hash())
	for _, mutate := range []func(*Vote){func(v *Vote) { v.Height++ }, func(v *Vote) { v.Round++ }, func(v *Vote) { v.Phase = Precommit }, func(v *Vote) { v.SetID[0] ^= 1 }, func(v *Vote) { v.Validator = ids[1] }, func(v *Vote) { v.BlockHash[0] ^= 1 }, func(v *Vote) { v.Signature[0] ^= 1 }} {
		bad := Clone(v)
		mutate(&bad)
		if f.set.VerifyVote(bad) == nil {
			t.Fatal("forged vote accepted")
		}
	}
	c := f.certificate(Precommit, 0, f.block, ids[:3])
	if f.set.VerifyDecision(Decision{Block: f.block, Certificate: c}) != nil {
		t.Fatal("valid certificate rejected")
	}
	for _, bad := range []Certificate{{Votes: c.Votes[:2]}, {Votes: []Vote{c.Votes[0], c.Votes[0], c.Votes[1]}}, f.certificate(Prevote, 0, f.block, ids[:3]), {Votes: []Vote{c.Votes[0], c.Votes[1], f.vote(ids[2], Precommit, 1, f.block.Hash())}}} {
		if f.set.VerifyCertificate(bad, Precommit, 1, f.block.Hash()) == nil {
			t.Fatal("bad certificate accepted")
		}
	}
	e := f.engines[ids[1]]
	if _, err := e.ReceiveVote(v); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReceiveVote(v); err != nil {
		t.Fatal("duplicate must be idempotent", err)
	}
	if _, err := e.ReceiveVote(f.vote(ids[0], Prevote, 0, crypto.Hash{})); err == nil {
		t.Fatal("equivocation accepted")
	}
	pubs := []ed25519.PublicKey{}
	for _, id := range ids {
		pubs = append(pubs, f.keys[id].Public().(ed25519.PublicKey))
	}
	other, err := NewValidatorSet(9002, f.chain.Tip().Hash(), pubs)
	if err != nil {
		t.Fatal(err)
	}
	if other.VerifyVote(v) == nil {
		t.Fatal("cross chain replay")
	}
	pubs[0] = pubs[1]
	if _, err := NewValidatorSet(9001, f.chain.Tip().Hash(), pubs); err == nil {
		t.Fatal("duplicate validator set")
	}
}

func TestLockSurvivesRestartAndHigherPOLPermitsProgress(t *testing.T) {
	f := setup(t)
	ids := f.set.IDs()
	id := ids[1]
	e := f.engines[id]
	p := f.proposal(0, f.block, nil)
	if _, err := e.ReceiveProposal(p); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.certificate(Prevote, 0, f.block, ids[:3]).Votes {
		if _, err := e.ReceiveVote(v); err != nil {
			t.Fatal(err)
		}
	}
	if e.State().Locked == nil || e.State().Decision != nil {
		t.Fatal("expected lock without commit")
	}
	e.Close()
	var err error
	e, err = NewEngine(f.set, f.keys[id], 1, f.journals[id], f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Timeout(1, 0); err != nil {
		t.Fatal(err)
	}
	other := f.block.Clone()
	other.Header.Timestamp = 2
	a, err := e.ReceiveProposal(f.proposal(1, other, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Votes) != 1 || a.Votes[0].BlockHash != (crypto.Hash{}) {
		t.Fatal("lost lock across restart/round")
	}
	if _, err := e.ReceiveProposal(f.proposal(1, f.block, nil)); err == nil {
		t.Fatal("conflicting proposal accepted")
	}
	if err := e.Timeout(1, 1); err != nil {
		t.Fatal(err)
	}
	// Other three validators (only one validator was locked in this fixture)
	// can establish a newer POL. The locked validator may then vote for it.
	pol := f.certificate(Prevote, 1, other, []string{ids[0], ids[2], ids[3]})
	a, err = e.ReceiveProposal(f.proposal(2, other, &pol))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Votes) != 1 || a.Votes[0].BlockHash != other.Hash() {
		t.Fatal("newer certified POL did not permit progress")
	}
	// Full old-round certificates remain usable after timer advancement.
	d := Decision{Block: other, Certificate: f.certificate(Precommit, 1, other, []string{ids[0], ids[2], ids[3]})}
	if err := e.ReceiveDecision(d); err != nil {
		t.Fatal(err)
	}
}

func TestPersistBeforeEmitAndRestartNeverDoubleSigns(t *testing.T) {
	f := setup(t)
	id := f.set.Proposer(1, 0)
	e := f.engines[id]
	j := f.journals[id]
	p, err := e.Propose(f.block)
	if err != nil {
		t.Fatal(err)
	}
	other := f.block.Clone()
	other.Header.Timestamp = 2
	e.Close()
	e, err = NewEngine(f.set, f.keys[id], 1, j, f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := e.Propose(other)
	if err != nil || string(repeated.Signature) != string(p.Signature) {
		t.Fatal("restart changed signed proposal", err)
	}
	j.fail = true
	if a, err := e.ReceiveProposal(*p); err == nil || len(a.Votes) != 0 {
		t.Fatal("vote escaped failed durable write")
	}
	j.fail = false
	if _, err := e.ReceiveProposal(*p); err == nil {
		t.Fatal("faulted signer resumed without reopen")
	}
	e, err = NewEngine(f.set, f.keys[id], 1, j, f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.ReceiveProposal(*p)
	if err != nil || len(a.Votes) != 1 {
		t.Fatal(err)
	}
	e.Close()
	e, err = NewEngine(f.set, f.keys[id], 1, j, f.chain.Validate)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := e.ReceiveProposal(*p)
	if err != nil || string(a.Votes[0].Signature) != string(retry.Votes[0].Signature) {
		t.Fatal("restart changed vote", err)
	}
	if _, err := NewEngine(f.set, f.keys[id], 2, j, f.chain.Validate); err == nil {
		t.Fatal("undecided height skipped")
	}
	j.state.Locked = &Decision{Block: f.block, Certificate: Certificate{}}
	if _, err := NewEngine(f.set, f.keys[id], 1, j, f.chain.Validate); err == nil {
		t.Fatal("corrupt lock restored")
	}
}

func TestInvalidExecutionAndProposalProofRejected(t *testing.T) {
	f := setup(t)
	e := f.engines[f.set.IDs()[1]]
	bad := f.block.Clone()
	bad.Header.StateRoot[0] ^= 1
	if _, err := e.ReceiveProposal(f.proposal(0, bad, nil)); err == nil {
		t.Fatal("invalid execution voted")
	}
	if err := e.Timeout(1, 0); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Certificate{f.certificate(Prevote, 1, f.block, f.set.IDs()[:3]), f.certificate(Precommit, 0, f.block, f.set.IDs()[:3]), f.certificate(Prevote, 0, f.block, f.set.IDs()[:2])} {
		if _, err := e.ReceiveProposal(f.proposal(1, f.block, &c)); err == nil {
			t.Fatal("invalid POL accepted")
		}
	}
}
