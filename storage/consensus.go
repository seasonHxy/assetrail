package storage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gochain/consensus"
)

func (s *Store) Certificates() []consensus.Certificate {
	return consensus.Clone(s.snapshot.Certificates)
}

func (s *Store) LoadConsensus() (*consensus.State, error) {
	f, err := os.Open(filepath.Join(s.dir, "consensus.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 16<<20 {
		return nil, fmt.Errorf("safety journal exceeds size limit")
	}
	dec := json.NewDecoder(io.LimitReader(f, 16<<20))
	dec.DisallowUnknownFields()
	var state consensus.State
	if err := dec.Decode(&state); err != nil {
		return nil, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("trailing safety journal data")
	}
	return &state, nil
}

// The node's mutex and the store's directory lock serialize this safety record
// with block commits. Any ambiguous write error poisons the store.
func (s *Store) SaveConsensus(state consensus.State) error {
	if s.lock == nil || !s.consensusMode {
		return fmt.Errorf("consensus store not open")
	}
	if s.fault != nil {
		return s.fault
	}
	old, err := s.LoadConsensus()
	if err != nil {
		return err
	}
	if err := consensus.ValidateTransition(*old, state); err != nil {
		return err
	}
	if err := s.writeFile("consensus.json", state); err != nil {
		s.fault = err
		return err
	}
	return nil
}
