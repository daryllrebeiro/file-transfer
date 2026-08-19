package transfer

import "encoding/binary"

const binaryHeaderSize = 16

// Binary chunks use a fixed header: uint64 chunk index, uint64 payload length,
// followed immediately by the payload. The WebSocket message itself identifies
// the transfer through its authenticated connection, so no repeated ID is needed.
func EncodeChunk(index uint64, payload []byte) []byte { frame := make([]byte, binaryHeaderSize+len(payload)); binary.BigEndian.PutUint64(frame[0:8], index); binary.BigEndian.PutUint64(frame[8:16], uint64(len(payload))); copy(frame[binaryHeaderSize:], payload); return frame }
func DecodeChunk(frame []byte) (uint64, []byte, bool) { if len(frame) < binaryHeaderSize { return 0, nil, false }; length := binary.BigEndian.Uint64(frame[8:16]); if length != uint64(len(frame)-binaryHeaderSize) { return 0, nil, false }; return binary.BigEndian.Uint64(frame[0:8]), frame[binaryHeaderSize:], true }