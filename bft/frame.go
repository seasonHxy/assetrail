package bft

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const MaxFrameBytes = 4 << 20

// One length-prefixed JSON request and response per authenticated libp2p stream.
func readFrame(r io.Reader, value any) error {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > MaxFrameBytes {
		return fmt.Errorf("invalid P2P frame size")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return fmt.Errorf("trailing P2P frame data")
	}
	return nil
}
func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return fmt.Errorf("P2P response too large")
	}
	frame := make([]byte, 4, len(data)+4)
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	_, err = io.Copy(w, bytes.NewReader(append(frame, data...)))
	return err
}
