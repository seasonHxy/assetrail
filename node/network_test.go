package node

import (
	"testing"
	"time"
)

func TestFollowerImportAuthorizationAndValidation(t *testing.T) {
	key, _, bob, g := setup(t)
	leader, err := OpenNetwork(t.TempDir(), g, "leader", "leader")
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	follower, err := OpenNetwork(t.TempDir(), g, "follower", "leader")
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	tx := txFor(t, key, bob, 0)
	_, _ = leader.Submit(tx)
	_, _ = follower.Submit(tx)
	if block, err := follower.Produce(time.Unix(1, 0)); err != nil || block != nil {
		t.Fatal("follower produced a block", err)
	}
	block, err := leader.Produce(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if follower.Import(*block, "other") == nil {
		t.Fatal("untrusted source accepted")
	}
	bad := block.Clone()
	bad.Header.StateRoot[0] ^= 1
	if follower.Import(bad, "leader") == nil {
		t.Fatal("invalid root accepted")
	}
	status, err := follower.Status()
	if err != nil || status.Height != 0 {
		t.Fatal("bad peer data poisoned node", err)
	}
	if err := follower.Import(*block, "leader"); err != nil {
		t.Fatal(err)
	}
	if err := follower.Import(*block, "leader"); err != nil {
		t.Fatal("exact duplicate was not idempotent", err)
	}
	if err := follower.Import(bad, "leader"); err == nil {
		t.Fatal("committed conflict accepted")
	}
	status, _ = follower.Status()
	if status.Height != 1 || status.Pending != 0 {
		t.Fatal(status)
	}
	if leader.Import(*block, "leader") == nil {
		t.Fatal("authority imports another producer's block")
	}
}

func TestNetworkBindingPreventsAccidentalModeChanges(t *testing.T) {
	_, _, _, g := setup(t)
	dir := t.TempDir()
	n, err := OpenNetwork(dir, g, "follower", "leader")
	if err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	if n, err := Open(dir, g); err == nil {
		_ = n.Close()
		t.Fatal("network data opened as solo")
	}
	for _, ids := range [][2]string{{"different", "leader"}, {"follower", "different"}} {
		if n, err := OpenNetwork(dir, g, ids[0], ids[1]); err == nil {
			_ = n.Close()
			t.Fatal("network binding changed")
		}
	}
	n, err = OpenNetwork(dir, g, "follower", "leader")
	if err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	key, _, bob, g := setup(t)
	dir = t.TempDir()
	n, err = Open(dir, g)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = n.Submit(txFor(t, key, bob, 0))
	_, err = n.Produce(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	if n, err := OpenNetwork(dir, g, "leader", "leader"); err == nil {
		_ = n.Close()
		t.Fatal("nonempty solo history adopted into P2P")
	}
}

func TestInventoryOrderingAndIsolation(t *testing.T) {
	key, _, bob, g := setup(t)
	n, err := Open(t.TempDir(), g)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	for _, nonce := range []uint64{2, 0, 1} {
		if _, err := n.Submit(txFor(t, key, bob, nonce)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := n.PendingPage(0, 2)
	if err != nil || len(page) != 2 || page[0].Nonce != 0 || page[1].Nonce != 1 {
		t.Fatal(page, err)
	}
	page[0].Signature[0] ^= 1
	page, _ = n.PendingPage(3, 1)
	if len(page) != 1 || page[0].Validate(g.ChainID) != nil {
		t.Fatal("page aliases pool")
	}
	block, err := n.Produce(time.Unix(1, 0))
	if err != nil || len(block.Transactions) != 3 {
		t.Fatal(err)
	}
	page, _ = n.PendingPage(0, 10)
	if len(page) != 0 {
		t.Fatal("inventory retains committed transactions")
	}
}
