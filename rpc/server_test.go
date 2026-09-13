package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/node"
)

func fixture(t *testing.T) (*node.Node, *crypto.PrivateKey, string, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := crypto.Address(key.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(bobKey.PubKey().SerializeCompressed())
	n, err := node.Open(t.TempDir(), blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{alice: math.MaxUint64}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n, key, alice, bob
}

func request(s http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestJSONRPCEnvelopeAndErrors(t *testing.T) {
	n, _, _, _ := fixture(t)
	s := New(n)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{`, -32700}, {`null`, -32600}, {`[]`, -32600}, {`1`, -32600},
		{`{"jsonrpc":"1.0","method":"chain_getStatus","id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":null,"id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"chain_getStatus","id":true}`, -32600},
		{`{"jsonrpc":"2.0","method":"chain_getStatus","params":null,"id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"unknown","id":1}`, -32601},
		{`{"jsonrpc":"2.0","method":"chain_getStatus","params":[1],"id":1}`, -32602},
		{`{"jsonrpc":"2.0","method":"chain_getBalance","params":[null],"id":1}`, -32602},
		{`{"jsonrpc":"2.0","method":"chain_getBlock","params":[1],"id":1}`, -32602},
		{`{"jsonrpc":"2.0","method":"chain_getBlock","params":["01"],"id":1}`, -32602},
	} {
		t.Run(tc.body, func(t *testing.T) {
			w := request(s, tc.body)
			var response map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var rpcErr Error
			if err := json.Unmarshal(response["error"], &rpcErr); err != nil {
				t.Fatal(w.Body.String())
			}
			if rpcErr.Code != tc.code {
				t.Fatal(w.Body.String())
			}
			if _, ok := response["result"]; ok {
				t.Fatal("error has result")
			}
		})
	}
	for _, id := range []string{`null`, `"client-1"`, `900719925474099312345`} {
		w := request(s, `{"jsonrpc":"2.0","method":"chain_getBlockNumber","id":`+id+`}`)
		var response map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if string(response["id"]) != id || string(response["result"]) != `"0"` {
			t.Fatal(w.Body.String())
		}
	}
	for _, body := range []string{`{"jsonrpc":"2.0","method":"chain_getStatus"}`, `[{"jsonrpc":"2.0","method":"unknown"}]`} {
		w := request(s, body)
		if w.Code != 204 || w.Body.Len() != 0 {
			t.Fatal("notification received response", w.Body.String())
		}
	}
	w := request(s, `[{"jsonrpc":"2.0","method":"chain_getStatus"},1,{"jsonrpc":"2.0","method":"chain_getBlockNumber","id":3}]`)
	var batch []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil || len(batch) != 2 {
		t.Fatal(w.Body.String(), err)
	}
}

func TestHTTPBoundsAndHealth(t *testing.T) {
	n, _, _, _ := fixture(t)
	s := New(n)
	for _, tc := range []struct {
		method, path, contentType, origin, body string
		status                                  int
	}{
		{"GET", "/", "", "", "", 405},
		{"POST", "/", "text/plain", "", "{}", 415},
		{"POST", "/", "application/json", "https://example.com", "{}", 403},
		{"POST", "/missing", "application/json", "", "{}", 404},
		{"GET", "/healthz", "", "", "", 200},
		{"POST", "/", "application/json", "", strings.Repeat(" ", MaxRequestBytes+1), 413},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	items := make([]string, MaxBatchSize+1)
	for i := range items {
		items[i] = `{"jsonrpc":"2.0","method":"chain_getStatus","id":1}`
	}
	w := request(s, "["+strings.Join(items, ",")+"]")
	if !strings.Contains(w.Body.String(), "-32600") {
		t.Fatal("batch limit ignored")
	}
	_ = n.Close()
	r := httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("closed node reported healthy")
	}
}

func TestRPCTransferReceiptProofAndUint64Precision(t *testing.T) {
	n, key, alice, bob := fixture(t)
	server := httptest.NewServer(New(n))
	defer server.Close()
	client := Client{URL: server.URL}
	ctx := context.Background()
	var balance Quantity
	if err := client.Call(ctx, "chain_getBalance", map[string]string{"address": alice}, &balance); err != nil || uint64(balance) != math.MaxUint64 {
		t.Fatal(balance, err)
	}
	const amount uint64 = 9007199254740993
	tx, err := (blockchain.Transaction{ChainID: 9001, To: bob, Amount: amount, GasLimit: blockchain.TransferGas}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRawTransaction(raw)
	if err != nil || decoded.Hash() != tx.Hash() {
		t.Fatal("raw roundtrip", err)
	}
	var hash crypto.Hash
	if err := client.Call(ctx, "chain_sendRawTransaction", []string{raw}, &hash); err != nil || hash != tx.Hash() {
		t.Fatal(hash, err)
	}
	if err := client.Call(ctx, "chain_sendRawTransaction", []string{raw}, &hash); err == nil {
		t.Fatal("duplicate accepted")
	}
	var receipt json.RawMessage
	if err := client.Call(ctx, "chain_getTransactionReceipt", []string{hash.String()}, &receipt); err != nil || string(receipt) != "null" {
		t.Fatal(string(receipt), err)
	}
	if _, err := n.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "chain_getBalance", []string{bob}, &balance); err != nil || uint64(balance) != amount {
		t.Fatal(balance, err)
	}
	if err := client.Call(ctx, "chain_getTransactionReceipt", []string{hash.String()}, &receipt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(receipt, []byte(`"status":"committed"`)) || !bytes.Contains(receipt, []byte(`"blockHeight":"1"`)) {
		t.Fatal(string(receipt))
	}
	var response struct {
		Root  crypto.Hash `json:"root"`
		Proof struct {
			Index    Quantity      `json:"index"`
			Total    Quantity      `json:"total"`
			Siblings []crypto.Hash `json:"siblings"`
		} `json:"proof"`
	}
	if err := client.Call(ctx, "chain_getTransactionProof", []string{hash.String()}, &response); err != nil {
		t.Fatal(err)
	}
	if !crypto.VerifyProof(hash[:], crypto.MerkleProof{Index: uint64(response.Proof.Index), Total: uint64(response.Proof.Total), Siblings: response.Proof.Siblings}, response.Root) {
		t.Fatal("invalid RPC proof")
	}
	var block json.RawMessage
	if err := client.Call(ctx, "chain_getBlock", []string{"latest"}, &block); err != nil || !bytes.Contains(block, []byte(`"amount":"9007199254740993"`)) {
		t.Fatal(string(block), err)
	}
	if err := client.Call(ctx, "chain_getBlock", []string{"999"}, &block); err != nil || string(block) != "null" {
		t.Fatal(string(block), err)
	}
	var info json.RawMessage
	if err := client.Call(ctx, "chain_getTransaction", []string{hash.String()}, &info); err != nil || !bytes.Contains(info, []byte(`"status":"committed"`)) {
		t.Fatal(string(info), err)
	}
}

func TestStrictQuantitiesAndRawTransactions(t *testing.T) {
	for _, value := range []string{`1`, `null`, `""`, `"+1"`, `"01"`, `"-1"`, `"1e3"`, `"18446744073709551616"`} {
		var q Quantity
		if json.Unmarshal([]byte(value), &q) == nil {
			t.Fatal("invalid quantity accepted", value)
		}
	}
	for _, value := range []string{"", "0xzz", "0x7b7d", "0x" + strings.Repeat("00", 9000)} {
		if _, err := DecodeRawTransaction(value); err == nil {
			t.Fatal("invalid raw tx accepted")
		}
	}
}

func TestClientRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{`{"jsonrpc":"2.0","id":2,"result":0}`, `{"jsonrpc":"2.0","id":1}`, `{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32000,"message":"x"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		client := Client{URL: server.URL}
		var result any
		if err := client.Call(context.Background(), "x", nil, &result); err == nil {
			t.Fatal("bad response accepted", body)
		}
		server.Close()
	}
}
