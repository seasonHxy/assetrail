package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"gochain/blockchain"
	"gochain/crypto"
	"gochain/p2p"
	"gochain/rpc"
	"gochain/wallet"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "settle:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: settle init | bft-init | identity | peer-id | address | sign | transfer | wait | export | verify | rpc")
	}
	if args[0] == "bft-init" {
		return initBFT(args[1:], out)
	}
	if args[0] == "export" || args[0] == "verify" {
		return runAudit(ctx, args, out)
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	endpoint := flags.String("rpc", "http://127.0.0.1:8545", "node RPC endpoint")
	keyPath := flags.String("key", ".settlelab/alice.key", "local private key file")
	client := rpc.Client{}
	parse := func() error {
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected positional arguments")
		}
		client.URL = *endpoint
		return nil
	}
	printJSON := func(value any) error { e := json.NewEncoder(out); e.SetIndent("", "  "); return e.Encode(value) }
	switch args[0] {
	case "peer-id":
		if err := parse(); err != nil {
			return err
		}
		key, err := p2p.LoadIdentity(*keyPath)
		if err != nil {
			return err
		}
		id, err := peer.IDFromPrivateKey(key)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, id.String())
		return err
	case "identity":
		path := flags.String("out", "", "new P2P identity file (0600)")
		if err := parse(); err != nil {
			return err
		}
		if *path == "" {
			return fmt.Errorf("identity requires --out")
		}
		id, err := p2p.CreateIdentity(*path)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, id.String())
		return err
	case "init":
		dir := flags.String("dir", ".settlelab", "new local devnet directory")
		if err := parse(); err != nil {
			return err
		}
		return initDevnet(*dir, out)
	case "address":
		if err := parse(); err != nil {
			return err
		}
		key, err := wallet.Load(*keyPath)
		if err != nil {
			return err
		}
		defer key.Zero()
		address, _ := crypto.Address(key.PubKey().SerializeCompressed())
		_, err = fmt.Fprintln(out, address)
		return err
	case "transfer", "sign":
		to := flags.String("to", "", "recipient address")
		amount := flags.String("amount", "", "amount in smallest integer units")
		price := flags.String("gas-price", "0", "gas price in smallest units")
		nonceText := flags.String("nonce", "", "explicit nonce; default is first gap in this node's pending nonces")
		chainText := flags.String("chain-id", "", "explicit chain ID for offline sign")
		wait := flags.Bool("wait", false, "wait for a durable receipt after submission")
		timeout := flags.Duration("timeout", 20*time.Second, "maximum receipt wait after submission")
		if err := parse(); err != nil {
			return err
		}
		if (*wait && args[0] == "sign") || *timeout <= 0 {
			return fmt.Errorf("--wait requires transfer and timeout must be positive")
		}
		if !crypto.ValidAddress(*to) {
			return fmt.Errorf("invalid recipient")
		}
		value, err := rpc.ParseQuantity(*amount)
		if err != nil {
			return err
		}
		gasPrice, err := rpc.ParseQuantity(*price)
		if err != nil {
			return err
		}
		key, err := wallet.Load(*keyPath)
		if err != nil {
			return err
		}
		defer key.Zero()
		address, _ := crypto.Address(key.PubKey().SerializeCompressed())
		var chainID, nonce rpc.Quantity
		if args[0] == "sign" {
			if *nonceText == "" || *chainText == "" {
				return fmt.Errorf("offline sign requires --nonce and --chain-id")
			}
			id, err := rpc.ParseQuantity(*chainText)
			if err != nil {
				return err
			}
			chainID = rpc.Quantity(id)
		} else {
			if *chainText != "" {
				return fmt.Errorf("--chain-id is only for offline sign")
			}
			if err := client.Call(ctx, "chain_getChainId", nil, &chainID); err != nil {
				return err
			}
		}
		if *nonceText == "" {
			if err := client.Call(ctx, "chain_getPendingNonce", []string{address}, &nonce); err != nil {
				return err
			}
		} else {
			value, err := rpc.ParseQuantity(*nonceText)
			if err != nil {
				return err
			}
			nonce = rpc.Quantity(value)
		}
		tx, err := (blockchain.Transaction{ChainID: uint64(chainID), To: *to, Amount: value, Nonce: uint64(nonce), GasLimit: blockchain.TransferGas, GasPrice: gasPrice}).Sign(key)
		if err != nil {
			return err
		}
		raw, err := rpc.EncodeRawTransaction(tx)
		if err != nil {
			return err
		}
		if args[0] == "sign" {
			_, err = fmt.Fprintln(out, raw)
			return err
		}
		var hash string
		if err := client.Call(ctx, "chain_sendRawTransaction", []string{raw}, &hash); err != nil {
			return err
		}
		if *wait {
			receipt, err := waitReceipt(ctx, client, hash, *timeout)
			if err != nil {
				return err
			}
			return printJSON(receipt)
		}
		_, err = fmt.Fprintln(out, hash)
		return err
	case "wait":
		hash := flags.String("hash", "", "submitted transaction hash")
		timeout := flags.Duration("timeout", 20*time.Second, "maximum time to wait for a receipt")
		if err := parse(); err != nil {
			return err
		}
		receipt, err := waitReceipt(ctx, client, *hash, *timeout)
		if err != nil {
			return err
		}
		return printJSON(receipt)
	case "rpc":
		method := flags.String("method", "chain_getStatus", "JSON-RPC method")
		params := flags.String("params", "[]", "JSON array or object of parameters")
		if err := parse(); err != nil {
			return err
		}
		if !json.Valid([]byte(*params)) {
			return fmt.Errorf("invalid params JSON")
		}
		var result json.RawMessage
		if err := client.Call(ctx, *method, json.RawMessage(*params), &result); err != nil {
			return err
		}
		return printJSON(result)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func initDevnet(dir string, out io.Writer) error {
	// Refuse existing directories so re-running init cannot replace keys/genesis.
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	addresses := make(map[string]string)
	for _, name := range []string{"alice", "bob"} {
		key, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		address, _ := crypto.Address(key.PubKey().SerializeCompressed())
		err = wallet.Save(filepath.Join(dir, name+".key"), key)
		key.Zero()
		if err != nil {
			return err
		}
		addresses[name] = address
	}
	genesis := blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{addresses["alice"]: 1_000_000}}
	f, err := os.OpenFile(filepath.Join(dir, "genesis.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(genesis); err != nil {
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
	return json.NewEncoder(out).Encode(map[string]any{"name": "SettleLab", "directory": dir, "alice": addresses["alice"], "bob": addresses["bob"], "aliceBalance": "1000000"})
}
