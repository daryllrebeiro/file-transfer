package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"file-transfer/backend/internal/transfer"
	ws "github.com/gorilla/websocket"
)

type fakePeer struct {
	sendControlCalled bool
	sendControlData   []byte
	sendBinaryCalled  bool
	sendBinaryData    []byte
	closeCalled       bool
}

func (f *fakePeer) SendControl(data []byte) error {
	f.sendControlCalled = true
	f.sendControlData = append([]byte(nil), data...)
	return nil
}

func (f *fakePeer) SendBinary(data []byte) error {
	f.sendBinaryCalled = true
	f.sendBinaryData = append([]byte(nil), data...)
	return nil
}

func (f *fakePeer) Close() error {
	f.closeCalled = true
	return nil
}

func newTestHandler() (*transfer.Manager, *Handler) {
	manager := transfer.NewManager(time.Minute, 10*1024*1024, 1024*1024)
	handler := NewHandler(manager, map[string]bool{"http://localhost:5173": true}, nil)
	return manager, handler
}

func dialWebSocket(t *testing.T, handler http.Handler, path string) *ws.Conn {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + path
	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func readJSON(t *testing.T, conn *ws.Conn, target interface{}) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	if err := conn.ReadJSON(target); err != nil {
		t.Fatal(err)
	}
}

func readMessageType(t *testing.T, conn *ws.Conn, expected string) map[string]interface{} {
	t.Helper()
	for {
		var message map[string]interface{}
		readJSON(t, conn, &message)
		if message["type"] == expected {
			return message
		}
	}
}

func TestHandlerRejectsDisallowedOriginAtUpgrade(t *testing.T) {
	manager, handler := newTestHandler()
	session, _, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + session.ID
	headers := http.Header{}
	headers.Set("Origin", "http://evil.example.com")
	_, resp, err := ws.DefaultDialer.Dial(url, headers)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected upgrade to reject disallowed origin")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandlerAcceptsAllowedOrigin(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	conn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	var msg map[string]interface{}
	readJSON(t, conn, &msg)
	if msg["type"] != "transfer_offer" {
		t.Fatalf("expected transfer_offer, got %v", msg["type"])
	}
}

func TestHandlerRejectsInvalidJoinToken(t *testing.T) {
	manager, handler := newTestHandler()
	session, _, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	conn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": "bad"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	var msg map[string]interface{}
	readJSON(t, conn, &msg)
	if msg["type"] != "error" {
		t.Fatalf("expected error, got %v", msg["type"])
	}
}

func TestHandlerFullTransferFlow(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}

	readMessageType(t, senderConn, "transfer_offer")
	readMessageType(t, receiverConn, "transfer_offer")

	if err := receiverConn.WriteJSON(map[string]string{"type": "accept_transfer"}); err != nil {
		t.Fatal(err)
	}

	for {
		var msg map[string]interface{}
		readJSON(t, senderConn, &msg)
		if msg["type"] == "transfer_accepted" {
			break
		}
	}

	frame := transfer.EncodeChunk(0, []byte("hello"))
	if err := senderConn.WriteMessage(ws.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}

	var ackSender map[string]interface{}
	readJSON(t, senderConn, &ackSender)
	if ackSender["type"] != "chunk_ack" {
		t.Fatalf("sender expected chunk_ack, got %v", ackSender["type"])
	}

	_, data, err := receiverConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(frame) {
		t.Fatal("receiver did not get expected frame")
	}

	if err := senderConn.WriteJSON(map[string]string{"type": "transfer_complete"}); err != nil {
		t.Fatal(err)
	}
	var completeReceiver map[string]interface{}
	readJSON(t, receiverConn, &completeReceiver)
	if completeReceiver["type"] != "transfer_complete" {
		t.Fatalf("receiver expected transfer_complete, got %v", completeReceiver["type"])
	}
}

func TestHandlerDetachNotifiesPeer(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	receiverConn.Close()
	time.Sleep(100 * time.Millisecond)

	readMessageType(t, senderConn, "receiver_disconnected")
}

func TestHandlerRejectsTransferNotFound(t *testing.T) {
	_, handler := newTestHandler()
	server := httptest.NewServer(handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/doesnotexist"
	_, resp, err := ws.DefaultDialer.Dial(url, nil)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected not found")
	}
	if resp != nil && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandlerRejectsInvalidJoinMessageType(t *testing.T) {
	manager, handler := newTestHandler()
	session, _, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	conn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]string{"type": "unknown"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	var msg map[string]interface{}
	readJSON(t, conn, &msg)
	if msg["type"] != "error" {
		t.Fatalf("expected error, got %v", msg["type"])
	}
}

func TestHandlerRejectsTransferIdMismatch(t *testing.T) {
	manager, handler := newTestHandler()
	session, _, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	conn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer conn.Close()

	if err := conn.WriteJSON(map[string]string{"type": "sender_join", "transferId": "mismatch", "token": "any"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	var msg map[string]interface{}
	readJSON(t, conn, &msg)
	if msg["type"] != "error" {
		t.Fatalf("expected error, got %v", msg["type"])
	}
}

func TestHandlerRoutesWebRTCMessages(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	if err := senderConn.WriteJSON(map[string]string{"type": "webrtc_offer", "sdp": "offer-sdp"}); err != nil {
		t.Fatal(err)
	}
	for {
		var msg map[string]interface{}
		readJSON(t, receiverConn, &msg)
		if msg["type"] == "webrtc_offer" {
			if msg["sdp"] != "offer-sdp" {
				t.Fatalf("unexpected sdp: %v", msg["sdp"])
			}
			break
		}
	}

	if err := receiverConn.WriteJSON(map[string]string{"type": "webrtc_answer", "sdp": "answer-sdp"}); err != nil {
		t.Fatal(err)
	}
	for {
		var msg map[string]interface{}
		readJSON(t, senderConn, &msg)
		if msg["type"] == "webrtc_answer" {
			if msg["sdp"] != "answer-sdp" {
				t.Fatalf("unexpected sdp: %v", msg["sdp"])
			}
			break
		}
	}
}

func TestHandlerCancelPropagatesToBothPeers(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	if err := senderConn.WriteJSON(map[string]string{"type": "transfer_cancelled"}); err != nil {
		t.Fatal(err)
	}

	readMessageType(t, senderConn, "transfer_cancelled")
	readMessageType(t, receiverConn, "transfer_cancelled")
}

func TestHandlerPauseAndRewind(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 100, ChunkSize: 10})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	if err := receiverConn.WriteJSON(map[string]string{"type": "accept_transfer"}); err != nil {
		t.Fatal(err)
	}
	readMessageType(t, senderConn, "transfer_accepted")

	// Send a few chunks
	for i := 0; i < 3; i++ {
		frame := transfer.EncodeChunk(uint64(i), []byte("chunk-data"))
		if err := senderConn.WriteMessage(ws.BinaryMessage, frame); err != nil {
			t.Fatal(err)
		}
		readJSON(t, senderConn, &map[string]interface{}{})
		receiverConn.ReadMessage()
	}

	// Receiver pauses
	if err := receiverConn.WriteJSON(map[string]interface{}{"type": "pause"}); err != nil {
		t.Fatal(err)
	}
	readMessageType(t, senderConn, "paused")

	// Receiver requests rewind to chunk 1
	if err := receiverConn.WriteJSON(map[string]interface{}{"type": "rewind", "nextChunk": 1}); err != nil {
		t.Fatal(err)
	}
	var rewindAck map[string]interface{}
	readJSON(t, senderConn, &rewindAck)
	if rewindAck["type"] != "rewind_ack" || rewindAck["nextChunk"] != float64(1) {
		t.Fatalf("expected rewind_ack with nextChunk=1, got %v", rewindAck)
	}

	// Verify session state reflects rewind
	s, _ := manager.Get(session.ID)
	s.Mu.Lock()
	if s.NextChunk != 1 {
		t.Fatalf("expected NextChunk=1 after rewind, got %d", s.NextChunk)
	}
	s.Mu.Unlock()
}

func TestHandlerChunkSizeChange(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 1000, ChunkSize: 100})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	if err := receiverConn.WriteJSON(map[string]string{"type": "accept_transfer"}); err != nil {
		t.Fatal(err)
	}
	readMessageType(t, senderConn, "transfer_accepted")

	// Sender requests chunk size change (must be >= MinChunkSize = 256KB)
	newSize := 300 * 1024 // 300KB
	if err := senderConn.WriteJSON(map[string]interface{}{"type": "chunk_size_change", "chunkSize": newSize}); err != nil {
		t.Fatal(err)
	}

	// Send a chunk with new size - should be accepted
	frame := transfer.EncodeChunk(0, make([]byte, newSize))
	if err := senderConn.WriteMessage(ws.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	var ack map[string]interface{}
	readJSON(t, senderConn, &ack)
	if ack["type"] != "chunk_ack" {
		t.Fatalf("expected chunk_ack after size change, got %v", ack["type"])
	}

	// Verify session metadata updated
	s, _ := manager.Get(session.ID)
	s.Mu.Lock()
	if s.Metadata.ChunkSize != newSize {
		t.Fatalf("expected chunkSize=%d, got %d", newSize, s.Metadata.ChunkSize)
	}
	s.Mu.Unlock()
}

func TestHandlerMultipleChunksOutOfOrder(t *testing.T) {
	manager, handler := newTestHandler()
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 1000, ChunkSize: 100})
	if err != nil {
		t.Fatal(err)
	}

	senderConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer senderConn.Close()
	receiverConn := dialWebSocket(t, handler, "/ws/"+session.ID)
	defer receiverConn.Close()

	if err := senderConn.WriteJSON(map[string]string{"type": "sender_join", "transferId": session.ID, "token": tokens.SenderToken}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.WriteJSON(map[string]string{"type": "receiver_join", "transferId": session.ID, "token": tokens.ReceiverToken}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*ws.Conn{senderConn, receiverConn} {
		readMessageType(t, conn, "transfer_offer")
	}

	if err := receiverConn.WriteJSON(map[string]string{"type": "accept_transfer"}); err != nil {
		t.Fatal(err)
	}
	readMessageType(t, senderConn, "transfer_accepted")

	// Send chunks out of order: 2, 0, 1 (within parallel window)
	testData := []byte("test-data-12345678")
	for _, idx := range []uint64{2, 0, 1} {
		frame := transfer.EncodeChunk(idx, testData)
		if err := senderConn.WriteMessage(ws.BinaryMessage, frame); err != nil {
			t.Fatal(err)
		}
		readJSON(t, senderConn, &map[string]interface{}{})
	}

	// All three should be received by receiver in send order (2, 0, 1)
	// The server forwards immediately; the frontend handles reordering
	expectedOrder := []uint64{2, 0, 1}
	for _, expectedIdx := range expectedOrder {
		_, data, err := receiverConn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		expected := transfer.EncodeChunk(expectedIdx, testData)
		if len(data) != len(expected) {
			t.Fatalf("chunk %d length mismatch: got %d, expected %d", expectedIdx, len(data), len(expected))
		}
		for j := range data {
			if data[j] != expected[j] {
				t.Fatalf("chunk %d byte %d mismatch: got %d, expected %d", expectedIdx, j, data[j], expected[j])
			}
		}
	}
}
