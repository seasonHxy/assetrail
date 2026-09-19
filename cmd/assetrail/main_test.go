package main

import (
	"bytes"
	"encoding/json"
	"gochain/rollup"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIKeyAndRootRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "wallet.key")
	var out bytes.Buffer
	if err := run([]string{"keygen", "--file", key}, &out); err != nil {
		t.Fatal(err)
	}
	var generated map[string]string
	if err := json.Unmarshal(out.Bytes(), &generated); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"address", "--key", key}, &out); err != nil || out.String() != generated["address"]+"\n" {
		t.Fatal("address", err)
	}
	if err := run([]string{"keygen", "--file", key}, &out); err == nil {
		t.Fatal("key overwritten")
	}
	statePath := filepath.Join(dir, "state.json")
	s := rollup.State{}
	raw, _ := json.Marshal(s)
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"root", "--file", statePath}, &out); err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["root"] != s.Root().String() {
		t.Fatal("root mismatch")
	}
	if err := os.WriteFile(statePath, append(raw, []byte(` {}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"root", "--file", statePath}, &out); err == nil {
		t.Fatal("trailing input accepted")
	}
}
