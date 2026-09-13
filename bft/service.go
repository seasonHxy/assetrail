package bft

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sort"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"gochain/consensus"
	"gochain/node"
	"gochain/p2p"
)

const Protocol protocol.ID = "/settlelab/bft-exchange/1.0.0"

type Service struct {
	host      host.Host
	d         *driver
	peers     map[peer.ID]peer.AddrInfo
	interval  time.Duration
	mu        sync.Mutex
	closed    bool
	observed  map[peer.ID]p2p.PeerInfo
	incoming  chan struct{}
	handlers  sync.WaitGroup
	closeOnce sync.Once
}
type Info struct {
	Enabled   bool            `json:"enabled"`
	ID        string          `json:"id"`
	Addresses []string        `json:"addresses"`
	Peers     []p2p.PeerInfo  `json:"peers"`
	Consensus ConsensusStatus `json:"consensus"`
}

func New(n *node.Node, c Config, key libcrypto.PrivKey, listen string, interval, timeout time.Duration) (*Service, error) {
	if interval < 50*time.Millisecond || interval > 5*time.Second {
		return nil, fmt.Errorf("BFT exchange interval must be 50ms..5s")
	}
	set, members, err := c.Validate()
	if err != nil {
		return nil, err
	}
	signing, err := SigningKey(key)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range signing {
			signing[i] = 0
		}
	}()
	id := consensus.ValidatorID(signing.Public().(ed25519.PublicKey))
	own, ok := members[id]
	if !ok {
		return nil, fmt.Errorf("local key is not a validator")
	}
	status, err := n.Status()
	if err != nil {
		return nil, err
	}
	if status.Mode != "bft-experimental" || status.PeerID != id {
		return nil, fmt.Errorf("node signing identity mismatch")
	}
	if listen == "" {
		listen = own.Addrs[0].String()
	}
	d, err := newDriver(n, set, signing, timeout)
	if err != nil {
		return nil, err
	}
	h, err := libp2p.New(libp2p.Identity(key), libp2p.NoTransports, libp2p.Transport(tcp.NewTCPTransport), libp2p.ListenAddrStrings(listen), libp2p.DisableRelay(), libp2p.DisableMetrics(), libp2p.WithDialTimeout(time.Second), libp2p.UserAgent("settlelab/bft-experimental"))
	if err != nil {
		d.close()
		return nil, err
	}
	s := &Service{host: h, d: d, interval: interval, peers: map[peer.ID]peer.AddrInfo{}, observed: map[peer.ID]p2p.PeerInfo{}, incoming: make(chan struct{}, 8)}
	for _, m := range members {
		if m.ID != h.ID() {
			s.peers[m.ID] = m
		}
	}
	h.SetStreamHandler(Protocol, s.handle)
	return s, nil
}
func (s *Service) Info() Info {
	info := Info{Enabled: true, ID: s.host.ID().String(), Consensus: s.d.status()}
	for _, addr := range s.host.Addrs() {
		info.Addresses = append(info.Addresses, addr.String()+"/p2p/"+info.ID)
	}
	s.mu.Lock()
	for id := range s.peers {
		p := s.observed[id]
		p.ID = id.String()
		p.Connected = s.host.Network().Connectedness(id) == network.Connected
		info.Peers = append(info.Peers, p)
	}
	s.mu.Unlock()
	sort.Strings(info.Addresses)
	sort.Slice(info.Peers, func(i, j int) bool { return info.Peers[i].ID < info.Peers[j].ID })
	return info
}
func (s *Service) record(id peer.ID, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.observed[id]
	if err == nil {
		p.LastSuccess = time.Now().UTC().Format(time.RFC3339Nano)
		p.LastError = ""
	} else {
		p.LastError = err.Error()
	}
	s.observed[id] = p
}
func (s *Service) handle(stream network.Stream) {
	s.mu.Lock()
	_, member := s.peers[stream.Conn().RemotePeer()]
	if s.closed || !member {
		s.mu.Unlock()
		_ = stream.Reset()
		return
	}
	select {
	case s.incoming <- struct{}{}:
	default:
		s.mu.Unlock()
		_ = stream.Reset()
		return
	}
	s.handlers.Add(1)
	s.mu.Unlock()
	defer s.handlers.Done()
	defer func() { <-s.incoming }()
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	var req packet
	if err := readFrame(stream, &req); err != nil {
		_ = stream.Reset()
		return
	}
	s.d.mu.Lock()
	err := s.d.apply(req, nil)
	var reply packet
	if err == nil {
		reply, err = s.d.snapshot(req.Offset, &req)
	}
	s.d.mu.Unlock()
	if err == nil {
		err = writeFrame(stream, reply)
	}
	if err != nil {
		_ = stream.Reset()
	}
	s.record(stream.Conn().RemotePeer(), err)
}
func (s *Service) exchange(ctx context.Context, info peer.AddrInfo, offset int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.host.Connect(ctx, info); err != nil {
		return offset, err
	}
	stream, err := s.host.NewStream(ctx, info.ID, Protocol)
	if err != nil {
		return offset, err
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	s.d.mu.Lock()
	req, err := s.d.snapshot(offset, nil)
	s.d.mu.Unlock()
	if err != nil {
		return offset, err
	}
	if err = writeFrame(stream, req); err != nil {
		_ = stream.Reset()
		return offset, err
	}
	var reply packet
	if err = readFrame(stream, &reply); err != nil {
		_ = stream.Reset()
		return offset, err
	}
	s.d.mu.Lock()
	err = s.d.apply(reply, &req)
	s.d.mu.Unlock()
	if len(req.Transactions) == MaxTransactions || len(reply.Transactions) == MaxTransactions {
		offset += MaxTransactions
	} else {
		offset = 0
	}
	if offset > 4095 {
		offset = 0
	}
	return offset, err
}
func (s *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for _, info := range s.peers {
		workers.Add(1)
		go func(info peer.AddrInfo) {
			defer workers.Done()
			offset := 0
			delay := s.interval
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				var err error
				offset, err = s.exchange(ctx, info, offset)
				s.record(info.ID, err)
				if err != nil {
					delay *= 2
					if delay > 2*time.Second {
						delay = 2 * time.Second
					}
				} else {
					delay = s.interval
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(info)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := s.d.tick(now); err != nil {
				return err
			}
		}
	}
}
func (s *Service) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.host.RemoveStreamHandler(Protocol)
		err = s.host.Close()
		s.handlers.Wait()
		s.d.close()
	})
	return err
}
