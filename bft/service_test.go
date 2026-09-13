package bft

import (
	"context"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"gochain/consensus"
	"gochain/node"
)

func TestRealStreamAdmissionAndForgedVote(t *testing.T) {
	f := setupDrivers(t)
	ids := f.set.IDs()
	n, err := node.OpenConsensus(t.TempDir(), f.genesis, ids[0], f.set)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	service, err := New(n, f.config, f.keys[ids[0]], "/ip4/127.0.0.1/tcp/0", 100*time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, member := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonmember", true: "member-forged-vote"}[member], func(t *testing.T) {
			options := []libp2p.Option{libp2p.NoTransports, libp2p.Transport(tcp.NewTCPTransport), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay(), libp2p.DisableMetrics()}
			if member {
				options = append(options, libp2p.Identity(f.keys[ids[1]]))
			}
			h, err := libp2p.New(options...)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err = h.Connect(ctx, peer.AddrInfo{ID: service.host.ID(), Addrs: service.host.Addrs()}); err != nil {
				t.Fatal(err)
			}
			stream, err := h.NewStream(ctx, service.host.ID(), Protocol)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
			p, err := f.drivers[ids[1]].snapshot(0, nil)
			if err != nil {
				t.Fatal(err)
			}
			if member {
				a, err := f.drivers[ids[1]].e.NilVote(1, 0, consensus.Prevote)
				if err != nil {
					t.Fatal(err)
				}
				p.Votes = a.Votes
				p.Votes[0].Signature[0] ^= 1
			}
			writeErr := writeFrame(stream, p)
			var reply packet
			if writeErr == nil && readFrame(stream, &reply) == nil {
				t.Fatal("unauthorized/forged message received a successful response")
			}
			// Taking the driver's lock also waits for any admitted handler's validation.
			service.d.mu.Lock()
			state := service.d.e.State()
			service.d.mu.Unlock()
			if state.Round != 0 || state.Prevote != nil || state.Precommit != nil || state.Decision != nil {
				t.Fatal("rejected stream altered signing state")
			}
		})
	}
}
