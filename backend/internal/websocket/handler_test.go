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
	manager := transfer.NewManager(time.Minute, 100, 10)
	handler := NewHandler(manager, map[string]bool{"http://localhost:5173": true})
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

	var senderMsg map[string]interface{}
	readJSON(t, senderConn, &senderMsg)
	if senderMsg["type"] != "transfer_offer" {
		t.Fatalf("sender expected transfer_offer, got %v", senderMsg["type"])
	}

	var receiverMsg map[string]interface{}
	readJSON(t, receiverConn, &receiverMsg)
	if receiverMsg["type"] != "transfer_offer" {
		t.Fatalf("receiver expected transfer_offer, got %v", receiverMsg["type"])
	}

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
		var msg map[string]interface{}
		readJSON(t, conn, &msg)
		if msg["type"] != "transfer_offer" {
			t.Fatalf("expected transfer_offer, got %v", msg["type"])
		}
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
		var msg map[string]interface{}
		readJSON(t, conn, &msg)
		if msg["type"] != "transfer_offer" {
			t.Fatalf("expected transfer_offer, got %v", msg["type"])
		}
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
		var msg map[string]interface{}
		readJSON(t, conn, &msg)
		if msg["type"] != "transfer_offer" {
			t.Fatalf("expected transfer_offer, got %v", msg["type"])
		}
	}

	if err := senderConn.WriteJSON(map[string]string{"type": "transfer_cancelled"}); err != nil {
		t.Fatal(err)
	}

	readMessageType(t, senderConn, "transfer_cancelled")
	readMessageType(t, receiverConn, "transfer_cancelled")
}
