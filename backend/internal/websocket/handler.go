package websocket

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"file-transfer/backend/internal/transfer"
	ws "github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4 << 20
	peerQueueSize  = 4
)

type Handler struct {
	Manager        *transfer.Manager
	AllowedOrigins map[string]bool
	upgrader       ws.Upgrader
}

func NewHandler(manager *transfer.Manager, allowedOrigins map[string]bool) *Handler {
	return &Handler{
		Manager:        manager,
		AllowedOrigins: allowedOrigins,
		upgrader: ws.Upgrader{
			ReadBufferSize:  32 << 10,
			WriteBufferSize: 32 << 10,
			CheckOrigin: func(r *http.Request) bool {
				if origin := r.Header.Get("Origin"); origin != "" {
					return allowedOrigins[origin]
				}
				return true
			},
		},
	}
}

type control struct {
	Type          string            `json:"type"`
	TransferID    string            `json:"transferId,omitempty"`
	Metadata      transfer.Metadata `json:"metadata,omitempty"`
	ChunkIndex    uint64            `json:"chunkIndex"`
	Message       string            `json:"message,omitempty"`
	Token         string            `json:"token,omitempty"`
	NextChunk     uint64            `json:"nextChunk"`
	SDP           string            `json:"sdp,omitempty"`
	Candidate     interface{}       `json:"candidate,omitempty"`
	SdpMid        string            `json:"sdpMid,omitempty"`
	SdpMLineIndex *int              `json:"sdpMLineIndex,omitempty"`
}
type outbound struct {
	kind int
	data []byte
}
type peer struct {
	connection *ws.Conn
	outbound   chan outbound
	done       chan struct{}
	closeOnce  sync.Once
}

var errPeerQueueFull = errors.New("peer outbound queue is full")

func newPeer(connection *ws.Conn) *peer {
	return &peer{connection: connection, outbound: make(chan outbound, peerQueueSize), done: make(chan struct{})}
}
func (peer *peer) Start()                        { go peer.writePump() }
func (peer *peer) SendControl(data []byte) error { return peer.enqueue(ws.TextMessage, data) }
func (peer *peer) SendBinary(data []byte) error  { return peer.enqueue(ws.BinaryMessage, data) }
func (peer *peer) enqueue(kind int, data []byte) error {
	message := outbound{kind: kind, data: append([]byte(nil), data...)}
	select {
	case <-peer.done:
		return peerClosedError{}
	case peer.outbound <- message:
		return nil
	default:
		return errPeerQueueFull
	}
}
func (peer *peer) Close() error {
	peer.closeOnce.Do(func() { close(peer.done) })
	return nil
}
func (peer *peer) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer peer.connection.Close()
	for {
		select {
		case message := <-peer.outbound:
			if err := peer.connection.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := peer.connection.WriteMessage(message.kind, message.data); err != nil {
				return
			}
		case <-ticker.C:
			if err := peer.connection.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := peer.connection.WriteMessage(ws.PingMessage, nil); err != nil {
				return
			}
		case <-peer.done:
			for {
				select {
				case message := <-peer.outbound:
					if err := peer.connection.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
						return
					}
					if err := peer.connection.WriteMessage(message.kind, message.data); err != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

type peerClosedError struct{}

func (peerClosedError) Error() string { return "peer connection is closed" }

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/ws/")
	_, ok := handler.Manager.Get(id)
	if !ok {
		http.Error(response, "transfer not found", http.StatusNotFound)
		return
	}
	if !handler.Manager.AcquireConnection() {
		http.Error(response, "connection limit reached", http.StatusServiceUnavailable)
		return
	}
	connection, err := handler.upgrader.Upgrade(response, request, nil)
	if err != nil {
		handler.Manager.ReleaseConnection()
		return
	}
	defer handler.Manager.ReleaseConnection()
	connection.SetReadLimit(maxMessageSize)
	peer := newPeer(connection)
	peer.Start()
	role, err := handler.join(peer, id)
	if err != nil {
		_ = peer.SendControl(controlBytes(control{Type: "error", Message: friendlyError(err)}))
		_ = peer.Close()
		return
	}
	defer func() { _ = peer.Close(); handler.detach(id, role, peer) }()
	connection.SetReadDeadline(time.Now().Add(pongWait))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(pongWait)) })
	handler.loop(connection, id, role)
}

func (handler *Handler) join(peer *peer, id string) (transfer.Role, error) {
	var message control
	if err := peer.connection.ReadJSON(&message); err != nil {
		return "", err
	}
	if message.TransferID != "" && message.TransferID != id {
		return "", errors.New("transfer ID does not match connection")
	}
	var session *transfer.Session
	var err error
	var role transfer.Role
	switch message.Type {
	case "sender_join":
		role = transfer.SenderRole
		if err = handler.Manager.ValidateToken(id, role, message.Token); err != nil {
			return "", err
		}
		session, err = handler.Manager.AttachSender(id, peer)
	case "receiver_join":
		role = transfer.ReceiverRole
		if err = handler.Manager.ValidateToken(id, role, message.Token); err != nil {
			return "", err
		}
		session, err = handler.Manager.AttachReceiver(id, peer)
	default:
		return "", errors.New("invalid join message")
	}
	if err != nil {
		return "", err
	}
	nextChunk, err := handler.Manager.NextChunk(id)
	if err != nil {
		return "", err
	}
	if err := peer.SendControl(controlBytes(control{Type: "transfer_offer", Metadata: session.Snapshot().Metadata, NextChunk: nextChunk})); err != nil {
		return "", err
	}
	if role == transfer.ReceiverRole {
		sender, _, connected := handler.Manager.Connections(id)
		if connected && sender != nil {
			_ = sender.SendControl(controlBytes(control{Type: "receiver_connected"}))
		}
	}
	return role, nil
}
func (handler *Handler) loop(connection *ws.Conn, id string, role transfer.Role) {
	for {
		messageType, data, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if messageType == ws.BinaryMessage && role == transfer.SenderRole {
			if !handler.forward(id, data) {
				handler.Manager.RecordFailed()
				return
			}
			continue
		}
		if messageType == ws.TextMessage {
			var message control
			if json.Unmarshal(data, &message) == nil {
				handler.control(id, role, message)
			}
		}
	}
}
func (handler *Handler) forward(id string, frame []byte) bool {
	index, payload, ok := transfer.DecodeChunk(frame)
	if !ok {
		return false
	}
	receiver, sender, duplicate, err := handler.Manager.PrepareChunk(id, index, frame)
	if err != nil || receiver == nil {
		return false
	}
	if err := receiver.SendBinary(frame); err != nil {
		handler.Manager.RecordQueueSaturation()
		return false
	}
	handler.Manager.RecordBytesRelayed(len(payload))
	if !duplicate {
		handler.Manager.RecordChunk(id, index, frame)
	}
	return sender == nil || sender.SendControl(controlBytes(control{Type: "chunk_ack", ChunkIndex: index})) == nil
}
func (handler *Handler) control(id string, role transfer.Role, message control) {
	switch message.Type {
	case "accept_transfer":
		if role != transfer.ReceiverRole {
			return
		}
		session, err := handler.Manager.Accept(id)
		if err != nil {
			return
		}
		sender, _, ok := handler.Manager.Connections(id)
		if ok && sender != nil {
			_ = sender.SendControl(controlBytes(control{Type: "transfer_accepted", Metadata: session.Snapshot().Metadata}))
		}
	case "transfer_complete":
		if role != transfer.SenderRole {
			return
		}
		if _, err := handler.Manager.Complete(id); err != nil {
			return
		}
		handler.Manager.RecordCompleted()
		_, receiver, ok := handler.Manager.Connections(id)
		if ok && receiver != nil {
			_ = receiver.SendControl(controlBytes(control{Type: "transfer_complete"}))
		}
	case "reject_transfer", "transfer_cancelled":
		if _, err := handler.Manager.Cancel(id); err != nil {
			return
		}
		handler.Manager.RecordCancelled()
		sender, receiver, ok := handler.Manager.Connections(id)
		if !ok {
			return
		}
		message := controlBytes(control{Type: "transfer_cancelled"})
		if sender != nil {
			_ = sender.SendControl(message)
		}
		if receiver != nil {
			_ = receiver.SendControl(message)
		}
		if sender != nil {
			_ = sender.Close()
		}
		if receiver != nil {
			_ = receiver.Close()
		}
	case "webrtc_offer":
		if role != transfer.SenderRole {
			return
		}
		_, receiver, ok := handler.Manager.Connections(id)
		if ok && receiver != nil {
			_ = receiver.SendControl(controlBytes(message))
		}
	case "webrtc_answer":
		if role != transfer.ReceiverRole {
			return
		}
		sender, _, ok := handler.Manager.Connections(id)
		if ok && sender != nil {
			_ = sender.SendControl(controlBytes(message))
		}
	case "webrtc_ice_candidate":
		sender, receiver, ok := handler.Manager.Connections(id)
		if !ok {
			return
		}
		if role == transfer.SenderRole && receiver != nil {
			_ = receiver.SendControl(controlBytes(message))
		} else if role == transfer.ReceiverRole && sender != nil {
			_ = sender.SendControl(controlBytes(message))
		}
	case "webrtc_connected":
		session, ok := handler.Manager.Get(id)
		if ok {
			session.Mu.Lock()
			session.ActiveTransport = transfer.TransportWebRTC
			session.Mu.Unlock()
		}
		sender, receiver, ok := handler.Manager.Connections(id)
		if ok {
			if role == transfer.SenderRole && receiver != nil {
				_ = receiver.SendControl(controlBytes(message))
			} else if role == transfer.ReceiverRole && sender != nil {
				_ = sender.SendControl(controlBytes(message))
			}
		}
	case "webrtc_failed", "webrtc_fallback":
		session, ok := handler.Manager.Get(id)
		if ok {
			session.Mu.Lock()
			session.ActiveTransport = transfer.TransportRelay
			session.Mu.Unlock()
		}
		sender, receiver, ok := handler.Manager.Connections(id)
		if ok {
			if role == transfer.SenderRole && receiver != nil {
				_ = receiver.SendControl(controlBytes(message))
			} else if role == transfer.ReceiverRole && sender != nil {
				_ = sender.SendControl(controlBytes(message))
			}
		}
	}
}
func (handler *Handler) detach(id string, role transfer.Role, peer transfer.Peer) {
	session, detached := handler.Manager.Detach(id, role, peer)
	if !detached || session == nil {
		return
	}
	sender, receiver, ok := handler.Manager.Connections(id)
	if !ok {
		return
	}
	messageType := "sender_disconnected"
	if role == transfer.ReceiverRole {
		messageType = "receiver_disconnected"
	}
	message := controlBytes(control{Type: messageType})
	if role == transfer.SenderRole && receiver != nil {
		_ = receiver.SendControl(message)
	}
	if role == transfer.ReceiverRole && sender != nil {
		_ = sender.SendControl(message)
	}
}
func controlBytes(message control) []byte { data, _ := json.Marshal(message); return data }
func friendlyError(err error) string {
	switch {
	case errors.Is(err, transfer.ErrTransferNotFound):
		return "This transfer was not found."
	case errors.Is(err, transfer.ErrTransferExpired):
		return "This transfer has expired."
	case errors.Is(err, transfer.ErrRoleConnected):
		return "This transfer role is already connected."
	default:
		return "Could not join this transfer."
	}
}
