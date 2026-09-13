package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"gochain/consensus"
	"gochain/crypto"
	"os"
	"path/filepath"
)

// NetworkBinding pins this data directory to one peer and one trusted producer.
// It is not a validator set or a consensus certificate.
type NetworkBinding struct {
	Version       int    `json:"version"`
	PeerID        string `json:"peerId"`
	Authority     string `json:"authority"`
	ConsensusHash string `json:"consensusHash,omitempty"`
}

// BindNetwork is called while the store's exclusive directory lock is held.
func (s *Store) BindNetwork(wanted *NetworkBinding) error {
	data, err := os.ReadFile(filepath.Join(s.dir, "network.json"))
	if errors.Is(err, os.ErrNotExist) {
		if wanted == nil {
			if _, err := os.Stat(filepath.Join(s.dir, "consensus.json")); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("consensus journal present; refusing solo mode")
			}
			return nil
		}
		if wanted.PeerID == "" || (wanted.Version != 1 && wanted.Version != 2) || (wanted.Version == 1 && (wanted.Authority == "" || wanted.ConsensusHash != "")) || (wanted.Version == 2 && (wanted.Authority != "" || wanted.ConsensusHash == "")) {
			return fmt.Errorf("invalid network binding")
		}
		if len(s.snapshot.Blocks) != 0 {
			return fmt.Errorf("use a fresh data directory when enabling P2P")
		}
		if wanted.Version == 2 {
			var domain crypto.Hash
			if domain.UnmarshalText([]byte(wanted.ConsensusHash)) != nil {
				return fmt.Errorf("invalid consensus domain")
			}
			state, err := s.LoadConsensus()
			if errors.Is(err, os.ErrNotExist) {
				state = &consensus.State{Version: 1, SetID: domain, Validator: wanted.PeerID, Height: 1}
				if err = s.writeFile("consensus.json", state); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if state.SetID != domain || state.Validator != wanted.PeerID {
				return fmt.Errorf("existing safety journal belongs to another validator set")
			}
			s.consensusMode = true
		} else if _, err := os.Stat(filepath.Join(s.dir, "consensus.json")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("consensus journal present; refusing authority mode")
		}
		return s.writeFile("network.json", wanted)
	}
	if err != nil {
		return err
	}
	var saved NetworkBinding
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("invalid network binding: %w", err)
	}
	if wanted == nil || saved != *wanted {
		return fmt.Errorf("network identity or authority mismatch; use the original P2P configuration")
	}
	if wanted.Version == 2 {
		if _, err := s.LoadConsensus(); err != nil {
			return fmt.Errorf("missing or corrupt consensus safety journal: %w", err)
		}
		s.consensusMode = true
	}
	return nil
}
