package bftdemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gochain/consensus"
	"gochain/node"
	"gochain/rpc"
)

func TestSixConsensusScenarios(t *testing.T) {
	r, err := Run(t.TempDir())
	if err != nil || !r.Passed || len(r.Checks) != 6 {
		t.Fatal(r, err)
	}
}

func TestCertifiedCommitGatingReplayAndRPC(t *testing.T) {
	c, err := newCluster(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ids := c.set.IDs()
	tx, err := c.submit(0)
	if err != nil {
		t.Fatal(err)
	}
	n := c.nodes[ids[0]]
	if b, err := n.Produce(time.Unix(1, 0)); err != nil || b != nil {
		t.Fatal("solo production bypass", err)
	}
	p, err := c.proposal(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Import(p.Block, ids[0]); err == nil {
		t.Fatal("authority import bypass")
	}
	if _, err := n.NewConsensusEngine(c.keys[ids[0]]); err == nil {
		t.Fatal("second active signer accepted")
	}
	d, err := c.deliver(broadcast(p, ids), ids, false)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	bad := consensus.Clone(*d)
	bad.Certificate.Votes = bad.Certificate.Votes[:2]
	if err := n.CommitCertified(bad); err == nil {
		t.Fatal("two-vote certificate accepted")
	}
	bad = consensus.Clone(*d)
	bad.Certificate.Votes[2] = bad.Certificate.Votes[0]
	if err := n.CommitCertified(bad); err == nil {
		t.Fatal("duplicate certificate accepted")
	}
	if r, err := n.Receipt(tx.Hash()); err != nil || r != nil {
		t.Fatal("receipt before durable commit", err)
	}
	if err := c.commit(d, ids); err != nil {
		t.Fatal(err)
	}
	if err := n.CommitCertified(*d); err != nil {
		t.Fatal("idempotent import", err)
	}
	server := httptest.NewServer(rpc.New(n))
	defer server.Close()
	client := rpc.Client{URL: server.URL}
	var cert consensus.Certificate
	if err := client.Call(context.Background(), "chain_getCommitCertificate", []string{"1"}, &cert); err != nil {
		t.Fatal(err)
	}
	if err := c.set.VerifyCertificate(cert, consensus.Precommit, 1, d.Block.Hash()); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := client.Call(context.Background(), "chain_getTransactionReceipt", []string{tx.Hash().String()}, &receipt); err != nil || receipt["confirmationScope"] != "certificate-verified-local" {
		t.Fatal(receipt, err)
	}
	server.Close()
	if err := c.stop(ids[0]); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.dir, ids[0])
	if other, err := node.Open(dir, c.g); err == nil {
		other.Close()
		t.Fatal("consensus directory opened as solo")
	}
	if other, err := node.OpenNetwork(dir, c.g, ids[0], ids[0]); err == nil {
		other.Close()
		t.Fatal("consensus directory opened as authority")
	}
	if other, err := node.OpenConsensus(dir, c.g, ids[1], c.set); err == nil {
		other.Close()
		t.Fatal("journal signer changed")
	}
	if err := c.restart(ids[0]); err != nil {
		t.Fatal(err)
	}
	r, err := c.nodes[ids[0]].Receipt(tx.Hash())
	if err != nil || r == nil || r.ConfirmationScope != "certificate-verified-local" {
		t.Fatal("certified receipt not recovered", err)
	}
	if err := c.stop(ids[0]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chain.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	var certs []consensus.Certificate
	if err := json.Unmarshal(snapshot["certificates"], &certs); err != nil {
		t.Fatal(err)
	}
	certs[0].Votes[0].Signature[0] ^= 1
	snapshot["certificates"], _ = json.Marshal(certs)
	corrupt, _ := json.Marshal(snapshot)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := node.OpenConsensus(dir, c.g, ids[0], c.set); err == nil {
		other.Close()
		t.Fatal("corrupt committed certificate replayed")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "consensus.json")); err != nil {
		t.Fatal(err)
	}
	if other, err := node.OpenConsensus(dir, c.g, ids[0], c.set); err == nil {
		other.Close()
		t.Fatal("missing safety journal silently reset")
	}
}

func TestJournalFailurePreventsSigningAndMissingBindingDoesNotEnableSolo(t *testing.T) {
	c, err := newCluster(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	id := c.set.IDs()[0]
	if _, err := c.submit(0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.dir, id, "consensus.json")
	// Rename is guaranteed to fail when its destination is a nonempty directory.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blocker"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if p, err := c.proposal(1, 0); err == nil || p != nil {
		t.Fatal("proposal escaped failed journal write")
	}
	if _, err := c.nodes[id].Status(); err == nil {
		t.Fatal("faulted node still serves")
	}
	if err := c.stop(id); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.dir, id, "network.json")); err != nil {
		t.Fatal(err)
	}
	if n, err := node.Open(filepath.Join(c.dir, id), c.g); err == nil {
		n.Close()
		t.Fatal("journal-only directory opened as solo")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed open removed journal")
	}
}

func TestDecisionBeforeBlockWriteIsRecoverable(t *testing.T) {
	c, err := newCluster(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ids := c.set.IDs()
	if _, err := c.submit(0); err != nil {
		t.Fatal(err)
	}
	p, err := c.proposal(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.deliver(broadcast(p, ids), ids, false)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	// All engines have fsynced a decision, but no ledger has appended it yet.
	for _, id := range ids {
		if err := c.restart(id); err != nil {
			t.Fatal(err)
		}
		state := c.engines[id].State()
		if state.Decision == nil {
			t.Fatal("decision lost before block append")
		}
		if err := c.nodes[id].CommitCertified(*state.Decision); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.checkLedger(ids, 1, d.Block.Hash()); err != nil {
		t.Fatal(err)
	}
}

func TestCertifiedBlockWriteFailureNeverAcknowledges(t *testing.T) {
	c, err := newCluster(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ids := c.set.IDs()
	tx, err := c.submit(0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.proposal(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.deliver(broadcast(p, ids), ids, false)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	id := ids[0]
	path := filepath.Join(c.dir, id, "chain.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(path, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.nodes[id].CommitCertified(*d); err == nil {
		t.Fatal("failed block write acknowledged")
	}
	if r, err := c.nodes[id].Receipt(tx.Hash()); err == nil || r != nil {
		t.Fatal("receipt exposed after failed block write")
	}
	if err := c.stop(id); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.restart(id); err != nil {
		t.Fatal(err)
	}
	s, err := c.nodes[id].Status()
	if err != nil || s.Height != 0 {
		t.Fatal("failed append changed ledger", err)
	}
	state := c.engines[id].State()
	if state.Decision == nil {
		t.Fatal("prepared decision missing")
	}
	if err := c.nodes[id].CommitCertified(*state.Decision); err != nil {
		t.Fatal(err)
	}
	if err := c.checkLedger([]string{id}, 1, d.Block.Hash()); err != nil {
		t.Fatal(err)
	}
}
