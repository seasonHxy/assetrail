package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"gochain/audit"
	"gochain/blockchain"
	"gochain/crypto"
	"gochain/rpc"
)

func runAudit(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	if args[0] == "verify" {
		path := flags.String("file", "", "audit bundle file")
		anchor := flags.String("block-hash", "", "independently trusted block hash (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *path == "" || *anchor == "" {
			return fmt.Errorf("verify requires --file and --block-hash")
		}
		var hash crypto.Hash
		if err := hash.UnmarshalText([]byte(*anchor)); err != nil {
			return err
		}
		f, err := os.Open(*path)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := audit.Decode(f)
		if err != nil {
			return err
		}
		result, err := audit.Verify(b, hash)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	}
	endpoint := flags.String("rpc", "http://127.0.0.1:8545", "export source RPC")
	hashText := flags.String("hash", "", "committed transaction hash")
	path := flags.String("out", "", "new audit bundle file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return fmt.Errorf("export requires --hash and --out")
	}
	var hash crypto.Hash
	if err := hash.UnmarshalText([]byte(*hashText)); err != nil {
		return err
	}
	client := rpc.Client{URL: *endpoint}
	var info *struct {
		Hash        crypto.Hash    `json:"hash"`
		Transaction audit.Transfer `json:"transaction"`
		Status      string         `json:"status"`
	}
	if err := client.Call(ctx, "chain_getTransaction", []string{*hashText}, &info); err != nil {
		return err
	}
	if info == nil || info.Status != "committed" {
		return fmt.Errorf("transaction is not committed on this node")
	}
	var proof *struct {
		TransactionHash crypto.Hash `json:"transactionHash"`
		BlockHash       crypto.Hash `json:"blockHash"`
		BlockHeight     uint64      `json:"blockHeight,string"`
		Root            crypto.Hash `json:"root"`
		Proof           audit.Proof `json:"proof"`
	}
	if err := client.Call(ctx, "chain_getTransactionProof", []string{*hashText}, &proof); err != nil {
		return err
	}
	if proof == nil {
		return fmt.Errorf("missing committed transaction proof")
	}
	var block *struct {
		Hash   crypto.Hash  `json:"hash"`
		Header audit.Header `json:"header"`
	}
	if err := client.Call(ctx, "chain_getBlockHeader", []string{strconv.FormatUint(proof.BlockHeight, 10)}, &block); err != nil {
		return err
	}
	if block == nil {
		return fmt.Errorf("missing block header")
	}
	b := audit.Bundle{Version: audit.Version, Transaction: info.Transaction, Header: block.Header, Proof: proof.Proof}
	if info.Hash != hash || info.Transaction.Core().Hash() != hash || proof.TransactionHash != hash || proof.BlockHash != block.Hash || proof.Root != block.Header.TxRoot || proof.BlockHeight != block.Header.Height {
		return fmt.Errorf("inconsistent RPC export data")
	}
	// This only checks internal consistency. Trust in this hash is not inferred.
	if _, err := audit.Verify(b, (blockchain.Block{Header: block.Header.Core()}).Hash()); err != nil {
		return err
	}
	if (blockchain.Block{Header: block.Header.Core()}).Hash() != block.Hash {
		return fmt.Errorf("RPC header hash mismatch")
	}
	f, err := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := audit.Encode(f, b); err != nil {
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
	return json.NewEncoder(out).Encode(map[string]any{"file": *path, "transactionHash": hash, "claimedBlockHash": block.Hash, "requiresIndependentTrustAnchor": true})
}
