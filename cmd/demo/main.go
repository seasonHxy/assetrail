// Command demo exercises the complete V0.1 transaction lifecycle.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/mempool"
)

func main() {
	jsonOutput := flag.Bool("json", false, "print a machine-readable demo result")
	flag.Parse()
	if err := run(*jsonOutput); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(jsonOutput bool) error {
	const chainID uint64 = 9001
	aliceKey, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	defer aliceKey.Zero()
	bobKey, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	defer bobKey.Zero()
	alice, _ := crypto.Address(aliceKey.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(bobKey.PubKey().SerializeCompressed())
	chain, err := blockchain.NewChain(chainID, map[string]uint64{alice: 100})
	if err != nil {
		return err
	}
	pool, err := mempool.New(chainID, mempool.DefaultCapacity)
	if err != nil {
		return err
	}
	tx, err := (blockchain.Transaction{ChainID: chainID, To: bob, Amount: 20, Nonce: 0, GasLimit: blockchain.TransferGas}).Sign(aliceKey)
	if err != nil {
		return err
	}
	if err := pool.Add(tx, chain.Snapshot()); err != nil {
		return err
	}
	before := chain.Snapshot()
	selected := pool.Select(before, 100)
	block, err := chain.BuildBlock(selected, time.Now().Unix())
	if err != nil {
		return err
	}
	if err := chain.Append(block); err != nil {
		return err
	}
	after := chain.Snapshot()
	pool.Reconcile(after)
	leaves := blockchain.TransactionLeaves(block.Transactions)
	proof, err := crypto.BuildProof(leaves, 0)
	if err != nil {
		return err
	}
	verified := crypto.VerifyProof(leaves[0], proof, block.Header.TxRoot)
	if !verified || after.Account(alice).Balance != 80 || after.Account(bob).Balance != 20 || pool.Len() != 0 {
		return fmt.Errorf("demo invariant failed")
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{
			"version": "v0.1", "alice": alice, "bob": bob,
			"before":          map[string]blockchain.Account{"alice": before.Account(alice), "bob": before.Account(bob)},
			"after":           map[string]blockchain.Account{"alice": after.Account(alice), "bob": after.Account(bob)},
			"transactionHash": tx.Hash(), "blockHash": block.Hash(), "block": block,
			"proof": proof, "proofVerified": verified, "mempoolRemaining": pool.Len(),
		})
	}
	fmt.Printf("SettleLab — Chain Core Demo\n\nAlice: %s\nBob:   %s\n\n", alice, bob)
	fmt.Printf("1. Genesis     Alice=100, Bob=0, chainId=%d\n", chainID)
	fmt.Printf("2. Signed TX   %s\n", tx.Hash())
	fmt.Println("3. Mempool     Accepted; selected 1 executable transaction")
	fmt.Printf("4. Block       #%d %s\n", block.Header.Height, block.Hash())
	fmt.Printf("5. Execution   Alice=%d (nonce=%d), Bob=%d\n", after.Account(alice).Balance, after.Account(alice).Nonce, after.Account(bob).Balance)
	fmt.Printf("6. Tx Root     %s\n7. State Root  %s\n", block.Header.TxRoot, block.Header.StateRoot)
	fmt.Printf("8. Proof       Verified=%t; mempool remaining=%d\n", verified, pool.Len())
	fmt.Println("\nSingle-node execution; gasPrice=0 for this demo; state is held in memory.")
	return nil
}
