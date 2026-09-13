package bft

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameLimitsAndStrictDecode(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"unknown":1}`), []byte(`{} {}`)} {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(raw)))
		b.Write(raw)
		var p packet
		if readFrame(&b, &p) == nil {
			t.Fatal("invalid JSON frame accepted")
		}
	}
	for _, size := range []uint32{0, MaxFrameBytes + 1} {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, size)
		var p packet
		if readFrame(&b, &p) == nil {
			t.Fatal("invalid size accepted")
		}
	}
}
