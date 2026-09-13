package bft

import (
	"fmt"
	"time"

	"gochain/blockchain"
	"gochain/consensus"
	"gochain/crypto"
)

const MaxTransactions = 32
const MaxDecisions = 4

type packet struct {
	SetID        crypto.Hash              `json:"setId"`
	Height       uint64                   `json:"height,string"` // Last durable certified block, not voting height.
	TipHash      crypto.Hash              `json:"tipHash"`
	Offset       int                      `json:"offset"`
	Transactions []blockchain.Transaction `json:"transactions,omitempty"`
	Proposal     *consensus.Proposal      `json:"proposal,omitempty"`
	Votes        []consensus.Vote         `json:"votes,omitempty"`
	Notices      []consensus.RoundNotice  `json:"notices,omitempty"`
	Decisions    []consensus.Decision     `json:"decisions,omitempty"`
}

func (d *driver) validatePacket(p packet) error {
	if p.SetID != d.set.ID() || p.Offset < 0 || p.Offset > 4095 || len(p.Transactions) > MaxTransactions || len(p.Decisions) > MaxDecisions || len(p.Votes) > 8 || len(p.Notices) > 4 {
		return fmt.Errorf("invalid packet domain or limits")
	}
	if p.Proposal != nil && len(p.Proposal.Block.Transactions) > blockchain.MaxBlockTransactions {
		return fmt.Errorf("oversize proposal")
	}
	for _, v := range p.Votes {
		if err := d.set.VerifyVote(v); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, n := range p.Notices {
		if err := d.set.VerifyRoundNotice(n); err != nil {
			return err
		}
		if seen[n.Validator] {
			return fmt.Errorf("duplicate notice")
		}
		seen[n.Validator] = true
	}
	if p.Proposal != nil {
		if err := d.set.VerifyProposal(*p.Proposal); err != nil {
			return err
		}
	}
	return nil
}

// Caller holds d.mu throughout snapshot and processing. Network I/O never holds it.
func (d *driver) snapshot(offset int, request *packet) (packet, error) {
	status, err := d.n.Status()
	if err != nil {
		return packet{}, err
	}
	s, votes := d.e.Gossip()
	p := packet{SetID: d.set.ID(), Height: status.Height, TipHash: status.BlockHash, Offset: offset, Proposal: s.Proposal, Votes: votes}
	p.Transactions, err = d.n.PendingPage(offset, MaxTransactions)
	if err != nil {
		return p, err
	}
	own, err := d.e.RoundNotice()
	if err != nil {
		return p, err
	}
	d.notices[own.Validator] = own
	for _, id := range d.set.IDs() {
		if n, ok := d.notices[id]; ok && n.Height == s.Height {
			p.Notices = append(p.Notices, n)
		}
	}
	if request != nil && request.Height <= status.Height {
		ancestor, err := d.n.Block(request.Height)
		if err != nil {
			return p, err
		}
		if ancestor == nil || ancestor.Hash() != request.TipHash {
			return p, fmt.Errorf("conflicting peer history")
		}
		for h := request.Height; h < status.Height && len(p.Decisions) < MaxDecisions; {
			h++
			b, err := d.n.Block(h)
			if err != nil {
				return p, err
			}
			c, err := d.n.CommitCertificate(h)
			if err != nil {
				return p, err
			}
			if b == nil || c == nil {
				return p, fmt.Errorf("missing durable certified block")
			}
			p.Decisions = append(p.Decisions, consensus.Decision{Block: *b, Certificate: *c})
		}
	}
	return p, nil
}
func (d *driver) apply(p packet, request *packet) error {
	if d.fault != nil {
		return d.fault
	}
	if err := d.validatePacket(p); err != nil {
		d.rejected++
		return err
	}
	if request == nil && len(p.Decisions) != 0 {
		d.rejected++
		return fmt.Errorf("unsolicited history in request")
	}
	if request != nil {
		count := uint64(0)
		if p.Height > request.Height {
			count = p.Height - request.Height
		}
		if count > MaxDecisions {
			count = MaxDecisions
		}
		if uint64(len(p.Decisions)) != count {
			return fmt.Errorf("incomplete history page")
		}
		parent := request.TipHash
		for i, dec := range p.Decisions {
			if dec.Block.Header.Height != request.Height+uint64(i)+1 || dec.Block.Header.ParentHash != parent {
				return fmt.Errorf("noncontiguous history page")
			}
			if err := d.set.VerifyDecision(dec); err != nil {
				d.rejected++
				return err
			}
			parent = dec.Block.Hash()
		}
		if p.Height >= request.Height && p.Height-request.Height == count && parent != p.TipHash {
			return fmt.Errorf("history tip mismatch")
		}
		for _, dec := range p.Decisions {
			status, err := d.n.Status()
			if err != nil {
				return err
			}
			if dec.Block.Header.Height <= status.Height {
				b, err := d.n.Block(dec.Block.Header.Height)
				if err != nil {
					return err
				}
				if b == nil || b.Hash() != dec.Block.Hash() {
					return fmt.Errorf("conflict with committed history")
				}
				continue
			}
			// Validate untrusted execution before commit can disable the active signer.
			if err := d.n.ValidateProposal(dec.Block); err != nil {
				d.rejected++
				return err
			}
			if err := d.commit(dec); err != nil {
				d.fault = err
				return err
			}
		}
	}
	for _, tx := range p.Transactions {
		_, _ = d.n.Submit(tx)
	} // Duplicate/stale pool entries are normal.
	s := d.e.State()
	for _, n := range p.Notices {
		if n.Height != s.Height {
			continue
		}
		old, ok := d.notices[n.Validator]
		if !ok || old.Round < n.Round {
			d.notices[n.Validator] = n
		}
	}
	var notices []consensus.RoundNotice
	for _, n := range d.notices {
		if n.Height == s.Height {
			notices = append(notices, n)
		}
	}
	if err := d.e.CatchUpRound(notices); err != nil {
		d.fault = err
		return err
	}
	if d.e.State().Round > s.Round {
		d.catchups++
	}
	s = d.e.State()
	if p.Proposal != nil && p.Proposal.Block.Header.Height == s.Height && p.Proposal.Round == s.Round {
		if p.Proposal.Block.Header.Timestamp > time.Now().Unix()+5 {
			d.rejected++
		} else if _, err := d.e.ReceiveProposal(*p.Proposal); err != nil {
			d.rejected++
		}
	}
	for _, v := range p.Votes {
		if v.Height != s.Height || v.Round != s.Round {
			continue
		}
		if _, err := d.e.ReceiveVote(v); err != nil {
			d.rejected++
		}
	}
	if err := d.finish(); err != nil {
		d.fault = err
		return err
	}
	d.resetTimer(time.Now())
	return nil
}
