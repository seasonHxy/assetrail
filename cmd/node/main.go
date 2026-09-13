package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"crypto/ed25519"
	"github.com/libp2p/go-libp2p/core/peer"
	"gochain/bft"
	"gochain/blockchain"
	"gochain/consensus"
	"gochain/node"
	"gochain/p2p"
	"gochain/rpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("settlelab-node", flag.ContinueOnError)
	data := flags.String("data", ".settlelab/data", "chain data directory")
	genesisPath := flags.String("genesis", ".settlelab/genesis.json", "genesis configuration")
	listen := flags.String("listen", "127.0.0.1:8545", "HTTP RPC address")
	interval := flags.Duration("block-time", 100*time.Millisecond, "block check interval (at least 50ms; timestamps still use whole seconds)")
	identityPath := flags.String("p2p-key", "", "persistent network identity file; enables P2P")
	authorityText := flags.String("authority", "", "trusted producer peer ID (required in P2P mode)")
	peersText := flags.String("peers", "", "comma-separated /p2p/<id> bootstrap multiaddresses")
	p2pListen := flags.String("p2p-listen", "", "libp2p TCP listen multiaddress")
	p2pInterval := flags.Duration("p2p-interval", 200*time.Millisecond, "inventory and sync interval")
	validatorsPath := flags.String("validators", "", "four-validator BFT configuration (requires --p2p-key)")
	consensusTimeout := flags.Duration("consensus-timeout", 2*time.Second, "initial deadline per consensus phase (500ms..10s)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *interval < node.MinBlockInterval {
		return fmt.Errorf("invalid arguments or block-time below 50ms")
	}
	f, err := os.Open(*genesisPath)
	if err != nil {
		return err
	}
	var genesis blockchain.Genesis
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	err = dec.Decode(&genesis)
	if err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = fmt.Errorf("trailing genesis data")
		}
	}
	_ = f.Close()
	if err != nil {
		return err
	}
	var n *node.Node
	var mesh interface {
		Run(context.Context) error
		Close() error
	}
	var networkInfo func() any
	var addresses []string
	if *validatorsPath != "" {
		if *identityPath == "" || *authorityText != "" || *peersText != "" {
			return fmt.Errorf("BFT requires --p2p-key and forbids --authority/--peers")
		}
		cfg, cfgErr := bft.LoadConfig(*validatorsPath)
		if cfgErr != nil {
			return cfgErr
		}
		set, _, cfgErr := cfg.Validate()
		if cfgErr != nil {
			return cfgErr
		}
		key, keyErr := p2p.LoadIdentity(*identityPath)
		if keyErr != nil {
			return keyErr
		}
		signing, keyErr := bft.SigningKey(key)
		if keyErr != nil {
			return keyErr
		}
		id := consensus.ValidatorID(signing.Public().(ed25519.PublicKey))
		for i := range signing {
			signing[i] = 0
		}
		n, err = node.OpenConsensus(*data, genesis, id, set)
		if err == nil {
			var service *bft.Service
			service, err = bft.New(n, cfg, key, *p2pListen, *p2pInterval, *consensusTimeout)
			if err != nil {
				_ = n.Close()
			} else {
				mesh = service
				defer mesh.Close()
				networkInfo = func() any { return service.Info() }
				addresses = service.Info().Addresses
			}
		}
	} else if *identityPath == "" {
		if *authorityText != "" || *peersText != "" {
			return fmt.Errorf("--authority and --peers require --p2p-key")
		}
		n, err = node.Open(*data, genesis)
	} else {
		key, keyErr := p2p.LoadIdentity(*identityPath)
		if keyErr != nil {
			return keyErr
		}
		id, keyErr := peer.IDFromPrivateKey(key)
		if keyErr != nil {
			return keyErr
		}
		authority, keyErr := peer.Decode(*authorityText)
		if keyErr != nil {
			return fmt.Errorf("invalid --authority: %w", keyErr)
		}
		n, err = node.OpenNetwork(*data, genesis, id.String(), authority.String())
		if err == nil {
			var peers []string
			if *peersText != "" {
				for _, address := range strings.Split(*peersText, ",") {
					peers = append(peers, strings.TrimSpace(address))
				}
			}
			var service *p2p.Service
			service, err = p2p.New(n, p2p.Config{Identity: key, Listen: *p2pListen, Bootstrap: peers, Interval: *p2pInterval})
			if err != nil {
				_ = n.Close()
			} else {
				mesh = service
				defer mesh.Close()
				networkInfo = func() any { return service.Info() }
				addresses = service.Info().Addresses
			}
		}
	}
	if err != nil {
		return err
	}
	defer n.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return runCtx }, Handler: rpc.New(n, networkInfo), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	results := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		results <- err
	}()
	go func() { defer wg.Done(); results <- n.Run(runCtx, *interval) }()
	if mesh != nil {
		wg.Add(1)
		go func() { defer wg.Done(); results <- mesh.Run(runCtx) }()
	}
	status, _ := n.Status()
	log.Printf("SettleLab %s: RPC http://%s, chain=%d, restored height=%d", status.Mode, listener.Addr(), status.ChainID, status.Height)
	if mesh != nil {
		log.Printf("P2P identity=%s mode=%s addresses=%v", status.PeerID, status.Mode, addresses)
	}
	select {
	case <-ctx.Done():
	case err = <-results:
	}
	cancel()
	if mesh != nil {
		_ = mesh.Close()
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		if err == nil {
			err = shutdownErr
		}
	}
	wg.Wait()
	return err
}
