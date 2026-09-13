package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gochain/bft"
	"gochain/blockchain"
	"gochain/p2p"
)

func initBFT(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("bft-init", flag.ContinueOnError)
	dir := flags.String("dir", ".settlelab-bft", "new four-validator local devnet directory")
	portsText := flags.String("ports", "9100,9101,9102,9103", "four distinct local TCP ports")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	ports := strings.Split(*portsText, ",")
	if len(ports) != 4 {
		return fmt.Errorf("require four TCP ports")
	}
	seen := map[int]bool{}
	for _, p := range ports {
		v, err := strconv.Atoi(p)
		if err != nil || v < 1024 || v > 65535 || seen[v] {
			return fmt.Errorf("invalid or repeated TCP port")
		}
		seen[v] = true
	}
	var buf bytes.Buffer
	if err := initDevnet(*dir, &buf); err != nil {
		return err
	}
	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(*dir, "genesis.json"))
	if err != nil {
		return err
	}
	var g blockchain.Genesis
	if err = json.Unmarshal(raw, &g); err != nil {
		return err
	}
	chain, err := g.Chain()
	if err != nil {
		return err
	}
	cfg := bft.Config{Version: 1, ChainID: g.ChainID, GenesisHash: chain.Tip().Hash()}
	for i, p := range ports {
		path := filepath.Join(*dir, fmt.Sprintf("validator%d.key", i))
		id, err := p2p.CreateIdentity(path)
		if err != nil {
			return err
		}
		key, err := p2p.LoadIdentity(path)
		if err != nil {
			return err
		}
		pub, err := key.GetPublic().Raw()
		if err != nil {
			return err
		}
		cfg.Validators = append(cfg.Validators, bft.Member{PublicKey: hex.EncodeToString(pub), Address: "/ip4/127.0.0.1/tcp/" + p + "/p2p/" + id.String()})
	}
	if _, _, err = cfg.Validate(); err != nil {
		return err
	}
	path := filepath.Join(*dir, "validators.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	e := json.NewEncoder(f)
	e.SetIndent("", "  ")
	err = e.Encode(cfg)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	result["validatorsFile"] = path
	result["validators"] = cfg.Validators
	e = json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(result)
}
