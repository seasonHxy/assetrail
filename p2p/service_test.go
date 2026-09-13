package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"gochain/blockchain"
	"gochain/crypto"
	"gochain/node"
)

func identity(t *testing.T) (libcrypto.PrivKey, peer.ID) {
	t.Helper()
	key, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, id
}
func accounts(t *testing.T) (*crypto.PrivateKey, string, string, blockchain.Genesis) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := crypto.Address(key.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(bobKey.PubKey().SerializeCompressed())
	return key, alice, bob, blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{alice: 1000}}
}
func signedTx(t *testing.T, key *crypto.PrivateKey, bob string, nonce uint64) blockchain.Transaction {
	t.Helper()
	tx, err := (blockchain.Transaction{ChainID: 9001, To: bob, Amount: 10, Nonce: nonce, GasLimit: blockchain.TransferGas}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func openService(t *testing.T, g blockchain.Genesis, key libcrypto.PrivKey, authority peer.ID, bootstrap []string) (*node.Node, *Service) {
	t.Helper()
	id, _ := peer.IDFromPrivateKey(key)
	n, err := node.OpenNetwork(t.TempDir(), g, id.String(), authority.String())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(n, Config{Identity: key, Bootstrap: bootstrap, Interval: 100 * time.Millisecond})
	if err != nil {
		_ = n.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = n.Close() })
	return n, s
}
func runService(t *testing.T, s *Service) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = s.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("service did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestRealLibp2pGossipAndMultiPageCatchup(t *testing.T) {
	wallet, _, bob, g := accounts(t)
	keyA, idA := identity(t)
	leader, a := openService(t, g, keyA, idA, nil)
	runService(t, a)
	keyB, _ := identity(t)
	bNode, b := openService(t, g, keyB, idA, a.Info().Addresses)
	runService(t, b)
	keyC, _ := identity(t)
	cNode, c := openService(t, g, keyC, idA, append(a.Info().Addresses, b.Info().Addresses...))
	runService(t, c)
	tx := signedTx(t, wallet, bob, 0)
	if _, err := cNode.Submit(tx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { info, _ := leader.Transaction(tx.Hash()); return info != nil })
	if _, err := leader.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { x, _ := bNode.Status(); y, _ := cNode.Status(); return x.Height == 1 && y.Height == 1 })
	// Create a history longer than one response before the fourth node starts.
	for nonce := uint64(1); nonce < 9; nonce++ {
		if _, err := leader.Submit(signedTx(t, wallet, bob, nonce)); err != nil {
			t.Fatal(err)
		}
		if _, err := leader.Produce(time.Unix(int64(nonce+1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	keyD, _ := identity(t)
	dNode, d := openService(t, g, keyD, idA, a.Info().Addresses)
	runService(t, d)
	eventually(t, func() bool {
		x, _ := bNode.Status()
		y, _ := cNode.Status()
		z, _ := dNode.Status()
		return x.Height == 9 && y.Height == 9 && z.Height == 9
	})
	expected, _ := leader.Status()
	for _, n := range []*node.Node{bNode, cNode, dNode} {
		status, _ := n.Status()
		if status.StateRoot != expected.StateRoot || status.BlockHash != expected.BlockHash {
			t.Fatal("nodes diverged")
		}
		account, _ := n.Account(bob)
		if account.Balance != 90 {
			t.Fatal(account)
		}
		info, err := n.Transaction(tx.Hash())
		if err != nil || info.Receipt == nil {
			t.Fatal("missing synced receipt", err)
		}
	}
	if d.Info().ConnectedCount == "0" {
		t.Fatal("no authenticated connections")
	}
}

func TestHandshakeRejectsOtherGenesisAndAuthority(t *testing.T) {
	_, _, _, g := accounts(t)
	key, id := identity(t)
	_, s := openService(t, g, key, id, nil)
	req := exchangeRequest{Genesis: s.genesis, Authority: id.String()}
	if _, err := s.respond(req); err != nil {
		t.Fatal(err)
	}
	req.Genesis[0] ^= 1
	if _, err := s.respond(req); err == nil {
		t.Fatal("wrong genesis accepted")
	}
	req.Genesis = s.genesis
	req.Authority = "other"
	if _, err := s.respond(req); err == nil {
		t.Fatal("wrong authority accepted")
	}
	req.Authority = id.String()
	req.Transactions = make([]blockchain.Transaction, MaxTransactions+1)
	if _, err := s.respond(req); err == nil {
		t.Fatal("oversized inventory accepted")
	}
	// Exercise a mismatched genesis over a real authenticated connection too.
	_, _, _, otherGenesis := accounts(t)
	fKey, fID := identity(t)
	n, err := node.OpenNetwork(t.TempDir(), otherGenesis, fID.String(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	follower, err := New(n, Config{Identity: fKey, Bootstrap: s.Info().Addresses})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := follower.exchange(ctx, id, 0); err == nil {
		t.Fatal("network handshake accepted another genesis")
	}
}

func TestAuthorityCannotSilentlyRewriteCommittedHistory(t *testing.T) {
	wallet, _, bob, g := accounts(t)
	key, id := identity(t)
	leader, s := openService(t, g, key, id, nil)
	fKey, _ := identity(t)
	follower, f := openService(t, g, fKey, id, s.Info().Addresses)
	_, _ = leader.Submit(signedTx(t, wallet, bob, 0))
	original, err := leader.Produce(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// A valid different history was already committed by this follower.
	alternativeChain, _ := g.Chain()
	tx := signedTx(t, wallet, bob, 0)
	alternative, err := alternativeChain.BuildBlock([]blockchain.Transaction{tx}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.Import(alternative, id.String()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.exchange(ctx, id, 0); err == nil {
		t.Fatal("conflicting authority history accepted")
	}
	status, _ := follower.Status()
	if status.BlockHash == original.Hash() || status.BlockHash != alternative.Hash() {
		t.Fatal("committed tip replaced")
	}
}

func TestFramesAndIdentity(t *testing.T) {
	var buf bytes.Buffer
	req := exchangeRequest{Authority: "test"}
	if err := writeFrame(&buf, req); err != nil {
		t.Fatal(err)
	}
	var decoded exchangeRequest
	if err := readFrame(&buf, &decoded); err != nil || decoded.Authority != "test" {
		t.Fatal(err)
	}
	for _, size := range []uint32{0, MaxFrameBytes + 1} {
		buf.Reset()
		_ = binary.Write(&buf, binary.BigEndian, size)
		if readFrame(&buf, &decoded) == nil {
			t.Fatal("invalid frame size accepted")
		}
	}
	buf.Reset()
	_ = binary.Write(&buf, binary.BigEndian, uint32(10))
	buf.WriteString("{}")
	if readFrame(&buf, &decoded) == nil {
		t.Fatal("truncated frame accepted")
	}
	path := filepath.Join(t.TempDir(), "identity.key")
	id, err := CreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := peer.IDFromPrivateKey(key)
	if id != loaded {
		t.Fatal("identity changed")
	}
	if _, err := CreateIdentity(path); err == nil {
		t.Fatal("identity overwritten")
	}
}

func TestMalformedAuthorityRepliesDoNotCommit(t *testing.T) {
	wallet, _, bob, g := accounts(t)
	key, id := identity(t)
	leader, s := openService(t, g, key, id, nil)
	_, _ = leader.Submit(signedTx(t, wallet, bob, 0))
	if _, err := leader.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	fKey, _ := identity(t)
	follower, f := openService(t, g, fKey, id, s.Info().Addresses)
	for name, mutate := range map[string]func(*exchangeResponse){
		"missing ancestor":       func(r *exchangeResponse) { r.Blocks = r.Blocks[1:] },
		"height gap":             func(r *exchangeResponse) { r.Blocks[1].Header.Height = 2 },
		"wrong tip":              func(r *exchangeResponse) { r.TipHash[0] ^= 1 },
		"invalid execution root": func(r *exchangeResponse) { r.Blocks[1].Header.StateRoot[0] ^= 1; r.TipHash = r.Blocks[1].Hash() },
	} {
		t.Run(name, func(t *testing.T) {
			s.host.SetStreamHandler(Protocol, func(stream network.Stream) {
				defer stream.Close()
				_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
				var req exchangeRequest
				if readFrame(stream, &req) != nil {
					return
				}
				r, err := s.respond(req)
				if err != nil {
					return
				}
				mutate(&r)
				_ = writeFrame(stream, r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := f.exchange(ctx, id, 0); err == nil {
				t.Fatal("malformed authority response accepted")
			}
			status, err := follower.Status()
			if err != nil || status.Height != 0 {
				t.Fatal("failed sync changed state or poisoned node", err)
			}
		})
	}
}
