package bft

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"gochain/blockchain"
	"gochain/consensus"
	"gochain/crypto"
	"gochain/node"
)

type driverFixture struct {
	genesis blockchain.Genesis
	config  Config
	set     *consensus.ValidatorSet
	drivers map[string]*driver
	tx      blockchain.Transaction
	keys    map[string]libcrypto.PrivKey
}

func setupDrivers(t *testing.T) *driverFixture {
	t.Helper()
	wallet, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer wallet.Zero()
	address, _ := crypto.Address(wallet.PubKey().SerializeCompressed())
	g := blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{address: 1_000_000}}
	chain, err := g.Chain()
	if err != nil {
		t.Fatal(err)
	}
	f := &driverFixture{genesis: g, config: Config{Version: 1, ChainID: g.ChainID, GenesisHash: chain.Tip().Hash()}, drivers: map[string]*driver{}, keys: map[string]libcrypto.PrivKey{}}
	for i := 0; i < 4; i++ {
		key, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := key.GetPublic().Raw()
		if err != nil {
			t.Fatal(err)
		}
		id := hex.EncodeToString(pub)
		pid, err := peer.IDFromPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		f.config.Validators = append(f.config.Validators, Member{PublicKey: id, Address: fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", 19000+i, pid)})
		f.keys[id] = key
	}
	f.set, _, err = f.config.Validate()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range f.set.IDs() {
		n, err := node.OpenConsensus(t.TempDir(), g, id, f.set)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = n.Close() })
		raw, err := SigningKey(f.keys[id])
		if err != nil {
			t.Fatal(err)
		}
		d, err := newDriver(n, f.set, raw, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.close)
		f.drivers[id] = d
	}
	f.tx, err = (blockchain.Transaction{ChainID: g.ChainID, To: address, Amount: 1, GasLimit: blockchain.TransferGas}).Sign(wallet)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestInvalidConfigAndWrongPeerIdentity(t *testing.T) {
	f := setupDrivers(t)
	bad := consensus.Clone(f.config)
	bad.Validators[0].Address = bad.Validators[1].Address
	if _, _, err := bad.Validate(); err == nil {
		t.Fatal("peer substitution accepted")
	}
	bad = consensus.Clone(f.config)
	bad.Validators[0] = bad.Validators[1]
	if _, _, err := bad.Validate(); err == nil {
		t.Fatal("duplicate member accepted")
	}
	bad = consensus.Clone(f.config)
	bad.Version = 2
	if _, _, err := bad.Validate(); err == nil {
		t.Fatal("unknown config version accepted")
	}
}
func TestDriverTimersAndAuthenticatedRoundRecovery(t *testing.T) {
	f := setupDrivers(t)
	ids := f.set.IDs()
	d := f.drivers[ids[0]]
	for _, expected := range []string{consensus.Prevote, consensus.Precommit, "propose"} {
		if err := d.tick(d.deadline.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if phase(d.e.State()) != expected {
			t.Fatalf("expected %s, got %s", expected, phase(d.e.State()))
		}
	}
	if d.e.State().Round != 1 || d.timeouts != 3 {
		t.Fatal("timer did not advance exactly one round")
	}
	for _, id := range ids[1:3] {
		e := f.drivers[id].e
		for r := uint32(0); r < 3; r++ {
			if err := e.Timeout(1, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	p, err := d.snapshot(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Notices = nil
	for _, id := range ids[1:3] {
		n, err := f.drivers[id].e.RoundNotice()
		if err != nil {
			t.Fatal(err)
		}
		p.Notices = append(p.Notices, n)
	}
	if err := d.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	if d.e.State().Round != 3 || d.catchups != 1 || d.e.State().Prevote != nil {
		t.Fatal("driver failed to catch up")
	}
}
func TestForgedPacketsDoNotChangeJournal(t *testing.T) {
	f := setupDrivers(t)
	d := f.drivers[f.set.Proposer(1, 0)]
	if _, err := d.n.Submit(f.tx); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(time.Now()); err != nil {
		t.Fatal(err)
	}
	original, err := d.snapshot(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*packet){
		"wrong-set":        func(p *packet) { p.SetID[0] ^= 1 },
		"forged-vote":      func(p *packet) { p.Votes[0].Signature[0] ^= 1 },
		"forged-proposal":  func(p *packet) { p.Proposal.Signature[0] ^= 1 },
		"duplicate-notice": func(p *packet) { p.Notices = append(p.Notices, p.Notices[0]) },
		"future-offset":    func(p *packet) { p.Offset = 4096 },
		"too-many-votes": func(p *packet) {
			for len(p.Votes) < 9 {
				p.Votes = append(p.Votes, p.Votes[0])
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := consensus.Clone(original)
			mutate(&p)
			if err := d.apply(p, nil); err == nil {
				t.Fatal("bad packet accepted")
			}
			s := d.e.State()
			if s.Height != 1 || s.Round != 0 || s.Decision != nil || s.Precommit != nil {
				t.Fatal("bad packet changed consensus")
			}
			if s.Prevote.BlockHash != original.Proposal.Block.Hash() {
				t.Fatal("bad packet replaced signed vote")
			}
		})
	}
}
func TestPacketRelayAndCertifiedHistory(t *testing.T) {
	f := setupDrivers(t)
	ids := f.set.IDs()
	leader := f.drivers[f.set.Proposer(1, 0)]
	if _, err := leader.n.Submit(f.tx); err != nil {
		t.Fatal(err)
	}
	if err := leader.tick(time.Now()); err != nil {
		t.Fatal(err)
	}
	// Three live validators exchange packets; the fourth receives only history later.
	for pass := 0; pass < 5; pass++ {
		for _, from := range ids[:3] {
			for _, to := range ids[:3] {
				if from == to {
					continue
				}
				a, b := f.drivers[from], f.drivers[to]
				req, err := a.snapshot(0, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = b.apply(req, nil); err != nil {
					t.Fatal(err)
				}
				response, err := b.snapshot(0, &req)
				if err != nil {
					t.Fatal(err)
				}
				if err = a.apply(response, &req); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, id := range ids[:3] {
		s, err := f.drivers[id].n.Status()
		if err != nil || s.Height != 1 {
			t.Fatal("no certified commit", s, err)
		}
	}
	behind := f.drivers[ids[3]]
	req, err := behind.snapshot(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := leader.snapshot(0, &req)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Decisions) != 1 {
		t.Fatal("missing history")
	}
	forged := consensus.Clone(response)
	forged.Decisions[0].Certificate.Votes[0].Signature[0] ^= 1
	if err = behind.apply(forged, &req); err == nil {
		t.Fatal("forged certificate accepted")
	}
	corrupt := consensus.Clone(response)
	corrupt.Decisions[0].Block.Transactions[0].Amount++
	if err = behind.apply(corrupt, &req); err == nil {
		t.Fatal("certificate allowed corrupt execution body")
	}
	if err = behind.apply(response, &req); err != nil {
		t.Fatal(err)
	}
	s, err := behind.n.Status()
	if err != nil || s.Height != 1 {
		t.Fatal("history not committed")
	}
	c, err := behind.n.CommitCertificate(1)
	if err != nil || c == nil || f.set.VerifyCertificate(*c, consensus.Precommit, 1, s.BlockHash) != nil {
		t.Fatal("durable certificate invalid")
	}
	// A repeated response must not create a second active signer or reset height.
	if err = behind.apply(response, &req); err != nil {
		t.Fatal("duplicate history", err)
	}
}

// Ensure the configured libp2p identity really is the consensus signing key.
func TestSigningIdentity(t *testing.T) {
	f := setupDrivers(t)
	for id, key := range f.keys {
		raw, err := SigningKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if consensus.ValidatorID(raw.Public().(ed25519.PublicKey)) != id {
			t.Fatal("identity mismatch")
		}
	}
}
