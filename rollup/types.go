// Package rollup implements AssetRail's bounded RWA/payment state machine.
// Its byte encoding, secp256k1 signatures and roots match contracts/src/AssetRail.sol.
package rollup

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"gochain/crypto"
	"golang.org/x/crypto/sha3"
)

const Accounts = 8
const MaxOperations = 32
const OperationBytes = 173
const Pay uint8 = 0
const TransferRWA uint8 = 1
const DvP uint8 = 2
const WithdrawCash uint8 = 3
const WithdrawRWA uint8 = 4

// Quantity uses decimal strings in JSON so clients cannot silently lose precision.
type Quantity uint64

func (q Quantity) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(q), 10))
}
func (q *Quantity) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("quantity must be a decimal string")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s {
		return fmt.Errorf("invalid quantity")
	}
	*q = Quantity(n)
	return nil
}

type State struct {
	Cash  [Accounts]Quantity `json:"cash"`
	RWA   [Accounts]Quantity `json:"rwa"`
	Nonce [Accounts]Quantity `json:"nonce"`
}

func (s State) ABI() []byte {
	out := make([]byte, Accounts*3*32)
	i := 0
	for _, values := range [][Accounts]Quantity{s.Cash, s.RWA, s.Nonce} {
		for _, v := range values {
			binary.BigEndian.PutUint64(out[i+24:i+32], uint64(v))
			i += 32
		}
	}
	return out
}
func (s State) Root() crypto.Hash { return Keccak(s.ABI()) }
func Keccak(data []byte) crypto.Hash {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(data)
	var out crypto.Hash
	copy(out[:], h.Sum(nil))
	return out
}
func EthereumAddress(key *crypto.PrivateKey) string {
	h := Keccak(key.PubKey().SerializeUncompressed()[1:])
	return "0x" + hex.EncodeToString(h[12:])
}
func addressBytes(s string) ([]byte, error) {
	if !crypto.ValidAddress(s) {
		return nil, fmt.Errorf("expected lowercase Ethereum address")
	}
	return hex.DecodeString(s[2:])
}

type Config struct {
	L1ChainID Quantity `json:"l1ChainId"`
	Contract  string   `json:"contract"`
	Accounts  []string `json:"accounts"`
}

func (c Config) Validate() error {
	if c.L1ChainID == 0 || len(c.Accounts) == 0 || len(c.Accounts) > Accounts {
		return fmt.Errorf("invalid rollup config")
	}
	if _, err := addressBytes(c.Contract); err != nil {
		return err
	}
	if c.Contract == "0x"+strings.Repeat("0", 40) {
		return fmt.Errorf("zero contract")
	}
	seen := map[string]bool{}
	for _, a := range c.Accounts {
		if _, err := addressBytes(a); err != nil {
			return err
		}
		if seen[a] || a == "0x"+strings.Repeat("0", 40) {
			return fmt.Errorf("duplicate or zero account")
		}
		seen[a] = true
	}
	return nil
}

type Operation struct {
	Kind             uint8    `json:"kind"`
	From             uint8    `json:"from"`
	To               uint8    `json:"to"`
	Amount           Quantity `json:"amount"`
	Price            Quantity `json:"price"`
	Nonce            Quantity `json:"nonce"`
	CounterNonce     Quantity `json:"counterNonce"`
	Sequence         Quantity `json:"sequence"`
	Signature        string   `json:"signature"`
	CounterSignature string   `json:"counterSignature"`
}

func (o Operation) Payload() []byte {
	b := make([]byte, 43)
	b[0] = o.Kind
	b[1] = o.From
	b[2] = o.To
	for i, v := range []Quantity{o.Amount, o.Price, o.Nonce, o.CounterNonce, o.Sequence} {
		binary.BigEndian.PutUint64(b[3+i*8:11+i*8], uint64(v))
	}
	return b
}
func (o Operation) Digest(c Config) (crypto.Hash, error) {
	if err := c.Validate(); err != nil {
		return crypto.Hash{}, err
	}
	b := []byte("assetrail/payment/v1")
	chain := make([]byte, 32)
	binary.BigEndian.PutUint64(chain[24:], uint64(c.L1ChainID))
	b = append(b, chain...)
	addr, _ := addressBytes(c.Contract)
	b = append(b, addr...)
	return Keccak(append(b, o.Payload()...)), nil
}
func (o *Operation) Sign(c Config, key *crypto.PrivateKey, counter bool) error {
	digest, err := o.Digest(c)
	if err != nil {
		return err
	}
	index := o.From
	if counter {
		if o.Kind != DvP {
			return fmt.Errorf("counter signature only for DvP")
		}
		index = o.To
	}
	if int(index) >= len(c.Accounts) || EthereumAddress(key) != c.Accounts[index] {
		return fmt.Errorf("key does not match participant")
	}
	sig := ecdsa.SignCompact(key, digest[:], false)
	if sig[0] != 27 && sig[0] != 28 {
		return fmt.Errorf("unsupported recovery id")
	}
	raw := append(append([]byte(nil), sig[1:]...), sig[0])
	encoded := "0x" + hex.EncodeToString(raw)
	if counter {
		o.CounterSignature = encoded
	} else {
		o.Signature = encoded
	}
	return nil
}
func decodeSig(s string, optional bool) ([]byte, error) {
	if optional && s == "" {
		return make([]byte, 65), nil
	}
	if !strings.HasPrefix(s, "0x") {
		return nil, fmt.Errorf("signature must be hex")
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil || len(b) != 65 {
		return nil, fmt.Errorf("signature must be 65 bytes")
	}
	return b, nil
}
func (o Operation) Encode() ([]byte, error) {
	a, err := decodeSig(o.Signature, false)
	if err != nil {
		return nil, err
	}
	b, err := decodeSig(o.CounterSignature, o.Kind != DvP)
	if err != nil {
		return nil, err
	}
	return append(append(o.Payload(), a...), b...), nil
}
func DecodeOperations(raw []byte) ([]Operation, error) {
	if len(raw)%OperationBytes != 0 || len(raw)/OperationBytes > MaxOperations {
		return nil, fmt.Errorf("invalid operation bytes")
	}
	var out []Operation
	for len(raw) > 0 {
		b := raw[:OperationBytes]
		o := Operation{Kind: b[0], From: b[1], To: b[2]}
		values := []*Quantity{&o.Amount, &o.Price, &o.Nonce, &o.CounterNonce, &o.Sequence}
		for i, p := range values {
			*p = Quantity(binary.BigEndian.Uint64(b[3+i*8 : 11+i*8]))
		}
		o.Signature = "0x" + hex.EncodeToString(b[43:108])
		o.CounterSignature = "0x" + hex.EncodeToString(b[108:173])
		out = append(out, o)
		raw = raw[OperationBytes:]
	}
	return out, nil
}
