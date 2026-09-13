// Package bft connects the experimental four-validator core to authenticated
// libp2p streams and a wall-clock pacemaker. Membership is static and equal-weight.
package bft

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"gochain/consensus"
	"gochain/crypto"
)

type Member struct {
	PublicKey string `json:"publicKey"`
	Address   string `json:"address"`
}
type Config struct {
	Version     int         `json:"version"`
	ChainID     uint64      `json:"chainId,string"`
	GenesisHash crypto.Hash `json:"genesisHash"`
	Validators  []Member    `json:"validators"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return c, err
	}
	if len(raw) > 65536 {
		return c, fmt.Errorf("validator configuration too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("trailing configuration")
	}
	_, _, err = c.Validate()
	return c, err
}
func (c Config) Validate() (*consensus.ValidatorSet, map[string]peer.AddrInfo, error) {
	if c.Version != 1 || len(c.Validators) != 4 {
		return nil, nil, fmt.Errorf("require version 1 and four validators")
	}
	var keys []ed25519.PublicKey
	peers := map[string]peer.AddrInfo{}
	for _, m := range c.Validators {
		raw, err := hex.DecodeString(m.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize || hex.EncodeToString(raw) != m.PublicKey {
			return nil, nil, fmt.Errorf("noncanonical validator public key")
		}
		pub, err := libcrypto.UnmarshalEd25519PublicKey(raw)
		if err != nil {
			return nil, nil, err
		}
		id, err := peer.IDFromPublicKey(pub)
		if err != nil {
			return nil, nil, err
		}
		addr, err := ma.NewMultiaddr(m.Address)
		if err != nil {
			return nil, nil, err
		}
		info, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			return nil, nil, err
		}
		if info.ID != id {
			return nil, nil, fmt.Errorf("peer ID does not match validator public key")
		}
		peers[m.PublicKey] = *info
		keys = append(keys, ed25519.PublicKey(raw))
	}
	set, err := consensus.NewValidatorSet(c.ChainID, c.GenesisHash, keys)
	return set, peers, err
}
func SigningKey(key libcrypto.PrivKey) (ed25519.PrivateKey, error) {
	if key == nil || key.Type() != libcrypto.Ed25519 {
		return nil, fmt.Errorf("BFT requires an Ed25519 identity")
	}
	raw, err := key.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid signing key length")
	}
	return append(ed25519.PrivateKey(nil), raw...), nil
}
