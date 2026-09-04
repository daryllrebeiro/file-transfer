package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type TransportMode string

const (
	TransportAuto   TransportMode = "auto"
	TransportRelay  TransportMode = "relay"
	TransportWebRTC TransportMode = "webrtc"
)

type Metadata struct {
	FileName  string `json:"fileName"`
	FileSize  int64  `json:"fileSize"`
	MimeType  string `json:"mimeType"`
	ChunkSize int    `json:"chunkSize"`
	SHA256    string `json:"sha256,omitempty"`
	Transport string `json:"transport,omitempty"`
}
type Peer interface {
	SendControl([]byte) error
	SendBinary([]byte) error
	Close() error
}
type Session struct {
	ID                string        `json:"id"`
	Metadata          Metadata      `json:"metadata"`
	CreatedAt         time.Time     `json:"createdAt"`
	ExpiresAt         time.Time     `json:"expiresAt"`
	State             State         `json:"state"`
	SenderConnected   bool          `json:"senderConnected"`
	ReceiverConnected bool          `json:"receiverConnected"`
	Accepted          bool          `json:"accepted"`
	TransportMode     TransportMode `json:"transportMode,omitempty"`
	ActiveTransport   TransportMode `json:"activeTransport,omitempty"`
	Sender            Peer          `json:"-"`
	Receiver          Peer          `json:"-"`
	NextChunk         uint64        `json:"-"`
	accepted          map[uint64]bool
	SenderTokenHash   [32]byte `json:"-"`
	ReceiverTokenHash [32]byte `json:"-"`
	Mu                sync.Mutex
}

// ParallelWindow is how far ahead of the contiguous floor the server will
// admit (and the relay receiver will buffer) out-of-order chunk indices.
const ParallelWindow = 16

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
func (session *Session) expired(now time.Time) bool { return now.After(session.ExpiresAt) }
func (session *Session) closeConnections() {
	if session.Sender != nil {
		_ = session.Sender.Close()
	}
	if session.Receiver != nil {
		_ = session.Receiver.Close()
	}
}
func (session *Session) Transition(next State) error {
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return transition(&session.State, next)
}
func (session *Session) Expire(now time.Time) bool {
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if IsTerminal(session.State) || !session.expired(now) {
		return false
	}
	if err := transition(&session.State, Expired); err != nil {
		return false
	}
	session.closeConnections()
	return true
}
func (session *Session) Snapshot() Session {
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return Session{ID: session.ID, Metadata: session.Metadata, CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt, State: session.State, SenderConnected: session.SenderConnected, ReceiverConnected: session.ReceiverConnected, Accepted: session.Accepted, TransportMode: session.TransportMode, ActiveTransport: session.ActiveTransport}
}

func (session *Session) OnRoleAttached(role Role) error {
	switch role {
	case SenderRole:
		if session.SenderConnected {
			return ErrRoleConnected
		}
		session.SenderConnected = true
	case ReceiverRole:
		if session.ReceiverConnected {
			return ErrRoleConnected
		}
		session.ReceiverConnected = true
	default:
		return ErrInvalidRole
	}
	if session.SenderConnected && session.ReceiverConnected {
		next := WaitingForAccept
		if session.Accepted && session.State == Paused {
			next = Transferring
		}
		if session.State == WaitingForReceiver || session.State == Paused {
			if err := transition(&session.State, next); err != nil {
				return err
			}
		}
	}
	return nil
}

var errInvalidState = errors.New("invalid transfer state")
