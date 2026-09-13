// Package p2p provides TCP libp2p transaction fanout and authority-only block
// synchronization. Periodic inventory exchange repairs missed gossip on reconnect.
package p2p

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/node"

	libp2p "github.com/libp2p/go-libp2p"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
)

const Protocol protocol.ID = "/settlelab/exchange/1.0.0"
const MaxTransactions = 32
const MaxBlocks = 4 // Plus one already-known ancestor for continuity checks.
const MaxPeers = 32

type Config struct {
	Identity  libcrypto.PrivKey
	Listen    string
	Bootstrap []string
	Interval  time.Duration
}

type exchangeRequest struct {
	Genesis      crypto.Hash              `json:"genesis"`
	Authority    string                   `json:"authority"`
	From         uint64                   `json:"from"`
	WantBlocks   bool                     `json:"wantBlocks"`
	Offset       int                      `json:"offset"`
	Transactions []blockchain.Transaction `json:"transactions"`
}
type exchangeResponse struct {
	Genesis      crypto.Hash              `json:"genesis"`
	Authority    string                   `json:"authority"`
	Height       uint64                   `json:"height"`
	TipHash      crypto.Hash              `json:"tipHash"`
	Transactions []blockchain.Transaction `json:"transactions"`
	Blocks       []blockchain.Block       `json:"blocks"`
	Error        string                   `json:"error,omitempty"`
}

type PeerInfo struct {
	ID          string `json:"id"`
	Connected   bool   `json:"connected"`
	LastSuccess string `json:"lastSuccess,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}
type Info struct {
	Enabled        bool       `json:"enabled"`
	ID             string     `json:"id"`
	Authority      string     `json:"authority"`
	Addresses      []string   `json:"addresses"`
	Peers          []PeerInfo `json:"peers"`
	ConnectedCount string     `json:"connectedCount"`
}

type Service struct {
	host      host.Host
	node      *node.Node
	genesis   crypto.Hash
	authority peer.ID
	bootstrap map[peer.ID]peer.AddrInfo
	interval  time.Duration
	mu        sync.Mutex
	observed  map[peer.ID]PeerInfo
	offset    int
	closed    bool
	incoming  chan struct{}
	handlers  sync.WaitGroup
}

func New(n *node.Node, cfg Config) (*Service, error) {
	if cfg.Identity == nil {
		return nil, fmt.Errorf("missing P2P identity")
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.Interval < 50*time.Millisecond {
		return nil, fmt.Errorf("P2P interval too short")
	}
	if cfg.Listen == "" {
		cfg.Listen = "/ip4/127.0.0.1/tcp/0"
	}
	status, err := n.Status()
	if err != nil {
		return nil, err
	}
	id, err := peer.IDFromPrivateKey(cfg.Identity)
	if err != nil {
		return nil, err
	}
	authority, err := peer.Decode(status.Authority)
	if err != nil || status.PeerID != id.String() {
		return nil, fmt.Errorf("node network binding does not match P2P identity")
	}
	if len(cfg.Bootstrap) > MaxPeers {
		return nil, fmt.Errorf("too many bootstrap peers")
	}
	bootstrap := make(map[peer.ID]peer.AddrInfo)
	for _, text := range cfg.Bootstrap {
		address, err := ma.NewMultiaddr(text)
		if err != nil {
			return nil, err
		}
		info, err := peer.AddrInfoFromP2pAddr(address)
		if err != nil {
			return nil, err
		}
		if info.ID == id {
			return nil, fmt.Errorf("bootstrap cannot refer to self")
		}
		previous := bootstrap[info.ID]
		previous.ID = info.ID
		previous.Addrs = append(previous.Addrs, info.Addrs...)
		bootstrap[info.ID] = previous
	}
	if id != authority {
		if _, ok := bootstrap[authority]; !ok {
			return nil, fmt.Errorf("followers require the authority in --peers")
		}
	}
	manager, err := connmgr.NewConnManager(16, MaxPeers)
	if err != nil {
		return nil, err
	}
	h, err := libp2p.New(libp2p.Identity(cfg.Identity), libp2p.NoTransports, libp2p.Transport(tcp.NewTCPTransport), libp2p.ListenAddrStrings(cfg.Listen), libp2p.DisableRelay(), libp2p.DisableMetrics(), libp2p.ConnectionManager(manager), libp2p.WithDialTimeout(3*time.Second), libp2p.UserAgent("settlelab/0.2.0-dev"))
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	genesis, err := n.Block(0)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	s := &Service{host: h, node: n, genesis: genesis.Hash(), authority: authority, bootstrap: bootstrap, interval: cfg.Interval, observed: make(map[peer.ID]PeerInfo), incoming: make(chan struct{}, 8)}
	h.SetStreamHandler(Protocol, s.handle)
	return s, nil
}

func (s *Service) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make(map[peer.ID]bool)
	for id := range s.bootstrap {
		ids[id] = true
	}
	for _, id := range s.host.Network().Peers() {
		ids[id] = true
	}
	info := Info{Enabled: true, ID: s.host.ID().String(), Authority: s.authority.String(), Addresses: []string{}, Peers: []PeerInfo{}, ConnectedCount: "0"}
	for _, address := range s.host.Addrs() {
		info.Addresses = append(info.Addresses, address.String()+"/p2p/"+s.host.ID().String())
	}
	count := 0
	for id := range ids {
		p := s.observed[id]
		p.ID = id.String()
		p.Connected = s.host.Network().Connectedness(id) == network.Connected
		if p.Connected {
			count++
		}
		info.Peers = append(info.Peers, p)
	}
	sort.Strings(info.Addresses)
	sort.Slice(info.Peers, func(i, j int) bool { return info.Peers[i].ID < info.Peers[j].ID })
	info.ConnectedCount = strconv.Itoa(count)
	return info
}

func (s *Service) record(id peer.ID, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.observed[id]; !ok && len(s.observed) >= MaxPeers*2 {
		return
	}
	p := s.observed[id]
	p.ID = id.String()
	if err != nil {
		p.LastError = err.Error()
	} else {
		p.LastError = ""
		p.LastSuccess = time.Now().UTC().Format(time.RFC3339)
	}
	s.observed[id] = p
}

func (s *Service) handle(stream network.Stream) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = stream.Reset()
		return
	}
	s.handlers.Add(1)
	s.mu.Unlock()
	defer s.handlers.Done()
	select {
	case s.incoming <- struct{}{}:
		defer func() { <-s.incoming }()
	default:
		_ = stream.Reset()
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	var req exchangeRequest
	if err := readFrame(stream, &req); err != nil {
		_ = stream.Reset()
		return
	}
	response, err := s.respond(req)
	if err != nil {
		response = exchangeResponse{Error: err.Error()}
	}
	if err := writeFrame(stream, response); err != nil {
		_ = stream.Reset()
	}
}

func (s *Service) respond(req exchangeRequest) (exchangeResponse, error) {
	if req.Genesis != s.genesis || req.Authority != s.authority.String() {
		return exchangeResponse{}, fmt.Errorf("genesis or authority mismatch")
	}
	if len(req.Transactions) > MaxTransactions || req.Offset < 0 || req.Offset >= 4096 {
		return exchangeResponse{}, fmt.Errorf("invalid inventory bounds")
	}
	// Duplicates/stale nonces are normal during anti-entropy; all admission rules
	// still apply, and malformed transactions never reach committed state.
	for _, tx := range req.Transactions {
		_, _ = s.node.Submit(tx)
	}
	status, err := s.node.Status()
	if err != nil {
		return exchangeResponse{}, err
	}
	pending, err := s.node.PendingPage(req.Offset, MaxTransactions)
	if err != nil {
		return exchangeResponse{}, err
	}
	response := exchangeResponse{Genesis: s.genesis, Authority: s.authority.String(), Height: status.Height, TipHash: status.BlockHash, Transactions: pending}
	if req.WantBlocks && s.host.ID() == s.authority && req.From <= status.Height {
		for height := req.From; len(response.Blocks) < MaxBlocks+1; height++ {
			b, err := s.node.Block(height)
			if err != nil {
				return exchangeResponse{}, err
			}
			if b == nil {
				return exchangeResponse{}, fmt.Errorf("missing historical block")
			}
			response.Blocks = append(response.Blocks, *b)
			if height == status.Height {
				break
			}
		}
	}
	return response, nil
}

func (s *Service) exchange(ctx context.Context, id peer.ID, offset int) error {
	if info, ok := s.bootstrap[id]; ok {
		if err := s.host.Connect(ctx, info); err != nil {
			return err
		}
	}
	status, err := s.node.Status()
	if err != nil {
		return err
	}
	txs, err := s.node.PendingPage(offset, MaxTransactions)
	if err != nil {
		return err
	}
	stream, err := s.host.NewStream(ctx, id, Protocol)
	if err != nil {
		return err
	}
	defer stream.Close()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = stream.SetDeadline(deadline)
	request := exchangeRequest{Genesis: s.genesis, Authority: s.authority.String(), From: status.Height, WantBlocks: id == s.authority && s.host.ID() != s.authority, Offset: offset, Transactions: txs}
	if err := writeFrame(stream, request); err != nil {
		_ = stream.Reset()
		return err
	}
	var response exchangeResponse
	if err := readFrame(stream, &response); err != nil {
		_ = stream.Reset()
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("peer: %s", response.Error)
	}
	if response.Genesis != s.genesis || response.Authority != s.authority.String() {
		return fmt.Errorf("peer genesis or authority mismatch")
	}
	if len(response.Transactions) > MaxTransactions || len(response.Blocks) > MaxBlocks+1 {
		return fmt.Errorf("peer response exceeds bounds")
	}
	if request.WantBlocks {
		if response.Height < status.Height {
			return fmt.Errorf("authority is behind local committed height")
		}
		count := response.Height - status.Height
		if count > MaxBlocks {
			count = MaxBlocks
		}
		if uint64(len(response.Blocks)) != count+1 {
			return fmt.Errorf("incomplete authority sync response")
		}
		if len(response.Blocks) == 0 || response.Blocks[0].Header.Height != status.Height || response.Blocks[0].Hash() != status.BlockHash {
			return fmt.Errorf("authority history conflicts with local tip")
		}
		if response.Height == status.Height && response.TipHash != status.BlockHash {
			return fmt.Errorf("conflicting authority tip")
		}
		last := response.Blocks[len(response.Blocks)-1]
		if last.Header.Height == response.Height && last.Hash() != response.TipHash {
			return fmt.Errorf("authority tip does not match sync response")
		}
		for i, block := range response.Blocks[1:] {
			if block.Header.Height != status.Height+uint64(i)+1 || block.Header.Height > response.Height {
				return fmt.Errorf("noncontiguous sync response")
			}
			if err := s.node.Import(block, id.String()); err != nil {
				return err
			}
		}
	}
	for _, tx := range response.Transactions {
		_, _ = s.node.Submit(tx)
	}
	return nil
}

func (s *Service) round(ctx context.Context) {
	s.mu.Lock()
	offset := s.offset
	s.offset = (s.offset + MaxTransactions) % 4096
	s.mu.Unlock()
	ids := []peer.ID{}
	seen := make(map[peer.ID]bool)
	// Prioritize the authority and configured peers, then accept connected peers.
	if s.host.ID() != s.authority {
		ids = append(ids, s.authority)
		seen[s.authority] = true
	}
	for id := range s.bootstrap {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for _, id := range s.host.Network().Peers() {
		if !seen[id] && len(ids) < MaxPeers {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id peer.ID) {
			defer wg.Done()
			timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			s.record(id, s.exchange(timeout, id, offset))
		}(id)
	}
	wg.Wait()
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		changed := s.node.Changes()
		s.round(ctx)
		if _, err := s.node.Status(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-changed:
			// Coalesce bursts; bounded to one extra round per 50ms. Periodic
			// anti-entropy still repairs missed events and reconnects.
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.host.RemoveStreamHandler(Protocol)
	err := s.host.Close()
	s.handlers.Wait()
	return err
}
