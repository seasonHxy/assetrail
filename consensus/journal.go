package consensus

import "fmt"

// ValidateTransition is also enforced by the durable store, so two stale engine
// handles cannot overwrite an already recorded vote even if the caller bypasses
// the node's single-engine factory. This is not protection against disk rollback.
func ValidateTransition(old, next State) error {
	if old.Version != 1 || next.Version != 1 || old.SetID != next.SetID || old.Validator != next.Validator || old.Height == 0 || next.Height < old.Height || next.Round > MaxRound {
		return fmt.Errorf("invalid safety journal transition")
	}
	if next.Height != old.Height {
		if next.Height-old.Height != 1 || old.Decision == nil || old.Decision.Block.Header.Height != old.Height || next.Round != 0 || next.Prevote != nil || next.Precommit != nil || next.Proposal != nil || next.Locked != nil || next.Valid != nil {
			return fmt.Errorf("height advance requires a decision and fresh round state")
		}
		return nil
	}
	if next.Round < old.Round {
		return fmt.Errorf("safety journal round rollback")
	}
	if old.Decision != nil && (next.Decision == nil || next.Decision.Block.Hash() != old.Decision.Block.Hash()) {
		return fmt.Errorf("cannot replace or forget a decision")
	}
	for _, pair := range [][2]*Decision{{old.Locked, next.Locked}, {old.Valid, next.Valid}} {
		if pair[0] == nil {
			continue
		}
		if pair[1] == nil || len(pair[0].Certificate.Votes) < Quorum || len(pair[1].Certificate.Votes) < Quorum {
			return fmt.Errorf("cannot forget lock/POL")
		}
		a, b := pair[0].Certificate.Votes[0].Round, pair[1].Certificate.Votes[0].Round
		if b < a || (b == a && pair[0].Block.Hash() != pair[1].Block.Hash()) {
			return fmt.Errorf("lock/POL rollback or conflict")
		}
	}
	if next.Round == old.Round {
		for _, pair := range [][2]*Vote{{old.Prevote, next.Prevote}, {old.Precommit, next.Precommit}} {
			if pair[0] == nil {
				continue
			}
			if pair[1] == nil || string(voteBytes(*pair[0])) != string(voteBytes(*pair[1])) || string(pair[0].Signature) != string(pair[1].Signature) {
				return fmt.Errorf("cannot overwrite a signed vote")
			}
		}
		if old.Proposal != nil && (next.Proposal == nil || !bytesEqualProposal(*old.Proposal, *next.Proposal)) {
			return fmt.Errorf("cannot overwrite a signed proposal")
		}
	}
	return nil
}
