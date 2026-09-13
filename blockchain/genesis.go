package blockchain

// Genesis is shared configuration. Nodes must agree on its resulting block hash.
type Genesis struct {
	ChainID uint64            `json:"chainId"`
	Alloc   map[string]uint64 `json:"alloc"`
}

func (g Genesis) Chain() (*Chain, error) { return NewChain(g.ChainID, g.Alloc) }
