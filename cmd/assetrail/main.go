package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"gochain/crypto"
	"gochain/rollup"
	"gochain/wallet"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "assetrail:", err)
		os.Exit(1)
	}
}
func read(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: assetrail keygen | address | root | sign | batch | decode")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	file := f.String("file", "", "input JSON file")
	config := f.String("config", "", "L1 domain and approved accounts JSON")
	keyPath := f.String("key", "", "local 0600 key file")
	counter := f.Bool("counter", false, "add DvP buyer signature")
	data := f.String("data", "", "hex batch data")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	switch args[0] {
	case "keygen":
		key, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		defer key.Zero()
		if err = wallet.Save(*file, key); err != nil {
			return err
		}
		return e.Encode(map[string]string{"address": rollup.EthereumAddress(key)})
	case "address":
		key, err := wallet.Load(*keyPath)
		if err != nil {
			return err
		}
		defer key.Zero()
		_, err = fmt.Fprintln(out, rollup.EthereumAddress(key))
		return err
	case "root":
		var s rollup.State
		if err := read(*file, &s); err != nil {
			return err
		}
		return e.Encode(map[string]any{"root": s.Root(), "abi": "0x" + hex.EncodeToString(s.ABI())})
	case "sign":
		var c rollup.Config
		var op rollup.Operation
		if err := read(*config, &c); err != nil {
			return err
		}
		if err := read(*file, &op); err != nil {
			return err
		}
		key, err := wallet.Load(*keyPath)
		if err != nil {
			return err
		}
		defer key.Zero()
		if err = op.Sign(c, key, *counter); err != nil {
			return err
		}
		return e.Encode(op)
	case "batch":
		var b rollup.Batch
		if err := read(*file, &b); err != nil {
			return err
		}
		result, err := rollup.Execute(b)
		if err != nil {
			return err
		}
		return e.Encode(result)
	case "decode":
		if len(*data) < 2 || (*data)[:2] != "0x" {
			return fmt.Errorf("expected 0x batch bytes")
		}
		raw, err := hex.DecodeString((*data)[2:])
		if err != nil {
			return err
		}
		ops, err := rollup.DecodeOperations(raw)
		if err != nil {
			return err
		}
		return e.Encode(ops)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
