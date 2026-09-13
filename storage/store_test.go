package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gochain/blockchain"
)

func TestSnapshotSurvivesStaleTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	g := blockchain.Genesis{ChainID: 1}
	s, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Chain()
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.BuildBlock(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AppendWithCommit(b, s.Append); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := os.WriteFile(filepath.Join(dir, ".chain-interrupted.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	blocks := s.Blocks()
	if len(blocks) != 1 || blocks[0].Hash() != b.Hash() {
		t.Fatal("lost durable block")
	}
	blocks[0].Header.Height = 99
	if s.Blocks()[0].Header.Height != 1 {
		t.Fatal("snapshot exposed mutable history")
	}
}

func TestRejectTruncatedUnknownAndTrailingSnapshots(t *testing.T) {
	for _, value := range []string{`{"version":`, `{"version":2,"genesis":{"chainId":1},"blocks":[]}`, `{"version":1,"genesis":{"chainId":1},"blocks":[]} {}`, `{"version":1,"genesis":{"chainId":1},"blocks":[],"unknown":1}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "chain.json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if s, err := Open(dir, blockchain.Genesis{ChainID: 1}); err == nil {
			_ = s.Close()
			t.Fatal("invalid snapshot accepted", value)
		}
	}
}

func TestFailedRenamePreservesSnapshotAndPoisonsWriter(t *testing.T) {
	dir := t.TempDir()
	g := blockchain.Genesis{ChainID: 1}
	s, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(dir, "chain.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	} // Rename cannot replace a directory.
	c, _ := g.Chain()
	b, _ := c.BuildBlock(nil, 1)
	if err := s.Append(b); err == nil {
		t.Fatal("rename failure ignored")
	}
	if len(s.Blocks()) != 0 {
		t.Fatal("failed write changed snapshot")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s.Append(b) == nil {
		t.Fatal("poisoned writer resumed")
	}
	files, err := filepath.Glob(filepath.Join(dir, ".chain-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary files not cleaned", files, err)
	}
}

func TestStoreCopiesGenesis(t *testing.T) {
	address := "0x0000000000000000000000000000000000000001"
	g := blockchain.Genesis{ChainID: 1, Alloc: map[string]uint64{address: 10}}
	dir := t.TempDir()
	s, err := Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g.Alloc[address] = 999
	c, _ := s.snapshot.Genesis.Chain()
	b, _ := c.BuildBlock(nil, 1)
	if err := s.Append(b); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "chain.json"))
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Genesis.Alloc[address] != 10 {
		t.Fatal("genesis aliases input")
	}
}
