package crypto

import "testing"

func TestMerkleProofs(t *testing.T) {
	for size := 1; size <= 17; size++ {
		leaves := make([][]byte, size)
		for i := range leaves {
			leaves[i] = []byte{byte(i)}
		}
		root := MerkleRoot(leaves)
		for i := range leaves {
			proof, err := BuildProof(leaves, i)
			if err != nil {
				t.Fatal(err)
			}
			if !VerifyProof(leaves[i], proof, root) {
				t.Fatalf("size=%d index=%d", size, i)
			}
			if VerifyProof([]byte("tampered"), proof, root) {
				t.Fatal("accepted altered leaf")
			}
			bad := proof
			bad.Total++
			if VerifyProof(leaves[i], bad, root) {
				t.Fatal("accepted altered count")
			}
			bad = proof
			bad.Index = bad.Total
			if VerifyProof(leaves[i], bad, root) {
				t.Fatal("accepted invalid index")
			}
			if len(proof.Siblings) > 0 {
				bad = proof
				bad.Siblings = proof.Siblings[:len(proof.Siblings)-1]
				if VerifyProof(leaves[i], bad, root) {
					t.Fatal("accepted truncated proof")
				}
			}
		}
	}
	if _, err := BuildProof(nil, 0); err == nil {
		t.Fatal("accepted empty tree proof")
	}
	if VerifyProof(nil, MerkleProof{}, MerkleRoot(nil)) {
		t.Fatal("accepted zero-leaf proof")
	}
	a, b, c := []byte("a"), []byte("b"), []byte("c")
	if MerkleRoot([][]byte{a, b, c}) == MerkleRoot([][]byte{a, b, c, c}) {
		t.Fatal("root does not commit to count")
	}
	if MerkleRoot(nil) == MerkleRoot([][]byte{nil}) {
		t.Fatal("empty root equals empty leaf")
	}
}

func TestKeySignature(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.PubKey().SerializeCompressed()
	digest := Sum([]byte("hello"))
	sig := Sign(key, digest)
	if err := Verify(pub, sig, digest); err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, sig, Sum([]byte("altered"))); err == nil {
		t.Fatal("accepted wrong digest")
	}
	if err := Verify(pub, []byte{1, 2, 3}, digest); err == nil {
		t.Fatal("accepted malformed signature")
	}
	address, err := Address(pub)
	if err != nil || !ValidAddress(address) {
		t.Fatal(address, err)
	}
	other, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(other.PubKey().SerializeCompressed(), sig, digest); err == nil {
		t.Fatal("accepted wrong key")
	}
}

func FuzzVerifyProof(f *testing.F) {
	f.Add([]byte("leaf"), uint64(0), uint64(1))
	f.Add([]byte{}, ^uint64(0), ^uint64(0))
	f.Fuzz(func(t *testing.T, data []byte, index, total uint64) {
		VerifyProof(data, MerkleProof{Index: index, Total: total, Siblings: []Hash{Sum(data)}}, Hash{})
	})
}
