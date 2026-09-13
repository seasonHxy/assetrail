// Package storage implements an atomic local snapshot store. The node owns its
// lifetime and serializes writes. Each commit rewrites history (O(chain size)).
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gochain/blockchain"
	"gochain/consensus"
)

type Snapshot struct {
	Version      int                     `json:"version"`
	Genesis      blockchain.Genesis      `json:"genesis"`
	Blocks       []blockchain.Block      `json:"blocks"`
	Certificates []consensus.Certificate `json:"certificates,omitempty"`
}

type Store struct {
	dir           string
	lock          *os.File
	snapshot      Snapshot
	fault         error
	consensusMode bool
}

func Open(dir string, genesis blockchain.Genesis) (_ *Store, err error) {
	expected, err := genesis.Chain()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := acquireLock(filepath.Join(dir, "LOCK"))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: lock}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	f, err := os.Open(filepath.Join(dir, "chain.json"))
	if errors.Is(err, os.ErrNotExist) {
		alloc := make(map[string]uint64, len(genesis.Alloc))
		for a, b := range genesis.Alloc {
			alloc[a] = b
		}
		s.snapshot = Snapshot{Version: 1, Genesis: blockchain.Genesis{ChainID: genesis.ChainID, Alloc: alloc}, Blocks: []blockchain.Block{}}
		if err := s.write(s.snapshot); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s.snapshot); err != nil {
		return nil, fmt.Errorf("read chain snapshot: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing snapshot data")
	}
	if s.snapshot.Version != 1 {
		return nil, fmt.Errorf("unsupported storage version %d", s.snapshot.Version)
	}
	actual, err := s.snapshot.Genesis.Chain()
	if err != nil {
		return nil, err
	}
	if actual.Tip().Hash() != expected.Tip().Hash() {
		return nil, fmt.Errorf("genesis mismatch: data directory belongs to another chain")
	}
	return s, nil
}

func (s *Store) Blocks() []blockchain.Block {
	blocks := make([]blockchain.Block, len(s.snapshot.Blocks))
	for i, b := range s.snapshot.Blocks {
		blocks[i] = b.Clone()
	}
	return blocks
}

// Append must only receive a block validated by Chain.AppendWithCommit.
func (s *Store) Append(block blockchain.Block) error {
	if s.consensusMode {
		return fmt.Errorf("certified append required")
	}
	return s.append(block, nil)
}
func (s *Store) AppendCertified(block blockchain.Block, c consensus.Certificate) error {
	if !s.consensusMode {
		return fmt.Errorf("not a consensus store")
	}
	return s.append(block, &c)
}
func (s *Store) append(block blockchain.Block, certificate *consensus.Certificate) error {
	if s.lock == nil {
		return fmt.Errorf("store closed")
	}
	if s.fault != nil {
		return s.fault
	}
	next := s.snapshot
	next.Blocks = append(append([]blockchain.Block(nil), s.snapshot.Blocks...), block.Clone())
	if certificate != nil {
		next.Certificates = append(append([]consensus.Certificate(nil), s.snapshot.Certificates...), consensus.Clone(*certificate))
	}
	if err := s.write(next); err != nil {
		s.fault = err
		return err
	}
	s.snapshot = next
	return nil
}

func (s *Store) write(snapshot Snapshot) error {
	return s.writeFile("chain.json", snapshot)
}

func (s *Store) writeFile(target string, value any) error {
	f, err := os.CreateTemp(s.dir, ".chain-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := json.NewEncoder(f).Encode(value); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(s.dir, target)); err != nil {
		return err
	}
	// Once renamed, a subsequent failure is ambiguous. The caller must stop
	// serving and reopen, which will replay whichever snapshot is durable.
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}
