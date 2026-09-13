package rpc

import (
	"fmt"
	"testing"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/node"
	"gochain/storage"
)

// Includes HTTP handler parsing/JSON encoding, but not network latency. Fixture
// signing, execution, disk writes and restart are outside the measured region.
func BenchmarkReceiptRPC(b *testing.B) {
	for _, count := range []int{64, 1000} {
		b.Run(fmt.Sprintf("block_%d", count), func(b *testing.B) {
			key, err := crypto.GenerateKey()
			if err != nil {
				b.Fatal(err)
			}
			defer key.Zero()
			address, _ := crypto.Address(key.PubKey().SerializeCompressed())
			g := blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{address: 1000000}}
			chain, err := g.Chain()
			if err != nil {
				b.Fatal(err)
			}
			txs := make([]blockchain.Transaction, count)
			for i := range txs {
				txs[i], err = (blockchain.Transaction{ChainID: 9001, To: address, Amount: 1, Nonce: uint64(i), GasLimit: blockchain.TransferGas}).Sign(key)
				if err != nil {
					b.Fatal(err)
				}
			}
			block, err := chain.BuildBlock(txs, 1)
			if err != nil {
				b.Fatal(err)
			}
			dir := b.TempDir()
			store, err := storage.Open(dir, g)
			if err != nil {
				b.Fatal(err)
			}
			if err = store.Append(block); err != nil {
				b.Fatal(err)
			}
			if err = store.Close(); err != nil {
				b.Fatal(err)
			}
			n, err := node.Open(dir, g)
			if err != nil {
				b.Fatal(err)
			}
			defer n.Close()
			s := New(n)
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"chain_getTransactionReceipt","params":["%s"]}`, txs[count/2].Hash())
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				request(s, body)
			}
		})
	}
}
