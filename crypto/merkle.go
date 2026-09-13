package crypto

import (
	"encoding/binary"
	"fmt"
)

// Leaves and internal nodes use separate domains; the final root commits to
// the leaf count so duplicating the last leaf cannot preserve a tree's root.
func leafHash(value []byte) Hash { return Sum(append([]byte{0}, value...)) }
func parentHash(a, b Hash) Hash {
	buf := make([]byte, 1, 65)
	buf[0] = 1
	buf = append(buf, a[:]...)
	buf = append(buf, b[:]...)
	return Sum(buf)
}
func countedRoot(root Hash, count uint64) Hash {
	buf := make([]byte, 9, 41)
	buf[0] = 2
	binary.BigEndian.PutUint64(buf[1:], count)
	return Sum(append(buf, root[:]...))
}
func merkleLevel(leaves [][]byte) []Hash {
	level := make([]Hash, len(leaves))
	for i, leaf := range leaves {
		level[i] = leafHash(leaf)
	}
	return level
}
func nextLevel(level []Hash) []Hash {
	next := make([]Hash, 0, (len(level)+1)/2)
	for i := 0; i < len(level); i += 2 {
		right := level[i]
		if i+1 < len(level) {
			right = level[i+1]
		}
		next = append(next, parentHash(level[i], right))
	}
	return next
}
func MerkleRoot(leaves [][]byte) Hash {
	if len(leaves) == 0 {
		return countedRoot(Hash{}, 0)
	}
	level := merkleLevel(leaves)
	for len(level) > 1 {
		level = nextLevel(level)
	}
	return countedRoot(level[0], uint64(len(leaves)))
}

type MerkleProof struct {
	Index    uint64 `json:"index"`
	Total    uint64 `json:"total"`
	Siblings []Hash `json:"siblings"`
}

func BuildProof(leaves [][]byte, index int) (MerkleProof, error) {
	if index < 0 || index >= len(leaves) {
		return MerkleProof{}, fmt.Errorf("leaf index out of range")
	}
	p := MerkleProof{Index: uint64(index), Total: uint64(len(leaves))}
	level := merkleLevel(leaves)
	for len(level) > 1 {
		sibling := index ^ 1
		if sibling >= len(level) {
			sibling = index
		}
		p.Siblings = append(p.Siblings, level[sibling])
		index /= 2
		level = nextLevel(level)
	}
	return p, nil
}

func VerifyProof(leaf []byte, p MerkleProof, root Hash) bool {
	if p.Total == 0 || p.Index >= p.Total {
		return false
	}
	h := leafHash(leaf)
	index, width := p.Index, p.Total
	for _, sibling := range p.Siblings {
		if width <= 1 {
			return false
		}
		if index%2 == 0 {
			if index+1 == width && sibling != h {
				return false
			}
			h = parentHash(h, sibling)
		} else {
			h = parentHash(sibling, h)
		}
		index /= 2
		width = width/2 + width%2
	}
	return width == 1 && countedRoot(h, p.Total) == root
}
