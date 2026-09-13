// Package consensus implements an experimental, transport-independent two-phase
// voting core. It is not a complete Tendermint implementation or network service.
package consensus

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"gochain/blockchain"
	"gochain/crypto"
)

const Quorum = 3
const MaxRound uint32 = 1_000_000
const Prevote = "prevote"
const Precommit = "precommit"

type ValidatorSet struct {
	chainID uint64
	genesis crypto.Hash
	id      crypto.Hash
	ids     []string
	keys    map[string]ed25519.PublicKey
}

func ValidatorID(public ed25519.PublicKey) string { return hex.EncodeToString(public) }

// The first release deliberately supports exactly four equally weighted keys.
func NewValidatorSet(chainID uint64, genesis crypto.Hash, keys []ed25519.PublicKey) (*ValidatorSet, error) {
	if chainID == 0 || genesis == (crypto.Hash{}) || len(keys) != 4 {
		return nil, fmt.Errorf("require chain ID, genesis hash and exactly four validators")
	}
	s := &ValidatorSet{chainID: chainID, genesis: genesis, keys: make(map[string]ed25519.PublicKey)}
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid validator key")
		}
		id := ValidatorID(key)
		if _, ok := s.keys[id]; ok {
			return nil, fmt.Errorf("duplicate validator")
		}
		s.keys[id] = append(ed25519.PublicKey(nil), key...)
		s.ids = append(s.ids, id)
	}
	sort.Strings(s.ids)
	var buf bytes.Buffer
	buf.WriteString("settlelab/consensus-set/v1")
	_ = binary.Write(&buf, binary.BigEndian, chainID)
	buf.Write(genesis[:])
	for _, id := range s.ids {
		buf.Write(s.keys[id])
	}
	s.id = crypto.Sum(buf.Bytes())
	return s, nil
}
func (s *ValidatorSet) ID() crypto.Hash         { return s.id }
func (s *ValidatorSet) ChainID() uint64         { return s.chainID }
func (s *ValidatorSet) Genesis() crypto.Hash    { return s.genesis }
func (s *ValidatorSet) IDs() []string           { return append([]string(nil), s.ids...) }
func (s *ValidatorSet) Contains(id string) bool { _, ok := s.keys[id]; return ok }
func (s *ValidatorSet) Proposer(height uint64, round uint32) string {
	return s.ids[((height-1)%4+uint64(round)%4)%4]
}

type Vote struct {
	SetID     crypto.Hash `json:"setId"`
	Height    uint64      `json:"height,string"`
	Round     uint32      `json:"round,string"`
	Phase     string      `json:"phase"`
	BlockHash crypto.Hash `json:"blockHash"` // Zero means nil, never a commit.
	Validator string      `json:"validator"`
	Signature []byte      `json:"signature"`
}

func voteBytes(v Vote) []byte {
	var b bytes.Buffer
	b.WriteString("settlelab/consensus-vote/v1/")
	b.WriteString(v.Phase)
	b.WriteByte(0)
	b.Write(v.SetID[:])
	_ = binary.Write(&b, binary.BigEndian, v.Height)
	_ = binary.Write(&b, binary.BigEndian, v.Round)
	b.Write(v.BlockHash[:])
	b.WriteString(v.Validator)
	return b.Bytes()
}
func (s *ValidatorSet) VerifyVote(v Vote) error {
	key, ok := s.keys[v.Validator]
	if !ok || v.SetID != s.id || v.Height == 0 || v.Round > MaxRound || (v.Phase != Prevote && v.Phase != Precommit) {
		return fmt.Errorf("invalid vote domain, member, height, round or phase")
	}
	if !ed25519.Verify(key, voteBytes(v), v.Signature) {
		return fmt.Errorf("invalid vote signature")
	}
	return nil
}

type Certificate struct {
	Votes []Vote `json:"votes"`
}

func (s *ValidatorSet) VerifyCertificate(c Certificate, phase string, height uint64, hash crypto.Hash) error {
	if hash == (crypto.Hash{}) || len(c.Votes) < Quorum || len(c.Votes) > 4 {
		return fmt.Errorf("certificate requires 3..4 distinct votes for a non-nil block")
	}
	seen := make(map[string]bool)
	round := c.Votes[0].Round
	for _, v := range c.Votes {
		if err := s.VerifyVote(v); err != nil {
			return err
		}
		if seen[v.Validator] || v.Phase != phase || v.Height != height || v.Round != round || v.BlockHash != hash {
			return fmt.Errorf("duplicate or inconsistent certificate vote")
		}
		seen[v.Validator] = true
	}
	return nil
}

type Proposal struct {
	SetID     crypto.Hash      `json:"setId"`
	Round     uint32           `json:"round,string"`
	Block     blockchain.Block `json:"block"`
	POL       *Certificate     `json:"proofOfLock,omitempty"`
	Proposer  string           `json:"proposer"`
	Signature []byte           `json:"signature"`
}

func proposalBytes(p Proposal) []byte {
	var b bytes.Buffer
	b.WriteString("settlelab/consensus-proposal/v1")
	b.Write(p.SetID[:])
	_ = binary.Write(&b, binary.BigEndian, p.Round)
	hash := p.Block.Hash()
	b.Write(hash[:])
	// Bind the proof's round. Its block/height/phase and signatures are verified
	// separately; alternate voter subsets proving the same POL are equivalent.
	validRound := int64(-1)
	if p.POL != nil && len(p.POL.Votes) != 0 {
		validRound = int64(p.POL.Votes[0].Round)
	}
	_ = binary.Write(&b, binary.BigEndian, validRound)
	b.WriteString(p.Proposer)
	return b.Bytes()
}
func (s *ValidatorSet) VerifyProposal(p Proposal) error {
	if p.SetID != s.id || p.Round > MaxRound || p.Block.Header.Height == 0 || p.Block.Header.ChainID != s.chainID || len(p.Block.Transactions) > blockchain.MaxBlockTransactions || p.Proposer != s.Proposer(p.Block.Header.Height, p.Round) {
		return fmt.Errorf("invalid proposal domain, height or proposer")
	}
	if p.POL != nil {
		if err := s.VerifyCertificate(*p.POL, Prevote, p.Block.Header.Height, p.Block.Hash()); err != nil {
			return err
		}
		if p.POL.Votes[0].Round >= p.Round {
			return fmt.Errorf("proposal POL must be from an earlier round")
		}
	}
	if !ed25519.Verify(s.keys[p.Proposer], proposalBytes(p), p.Signature) {
		return fmt.Errorf("invalid proposal signature")
	}
	return nil
}

type Decision struct {
	Block       blockchain.Block `json:"block"`
	Certificate Certificate      `json:"certificate"`
}

func (s *ValidatorSet) VerifyDecision(d Decision) error {
	if d.Block.Header.ChainID != s.chainID {
		return fmt.Errorf("decision chain mismatch")
	}
	return s.VerifyCertificate(d.Certificate, Precommit, d.Block.Header.Height, d.Block.Hash())
}

// All protocol records contain only JSON-serializable value types. Copies keep
// callers from mutating signatures, blocks or certificates retained by the engine.
func Clone[T any](value T) T {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
