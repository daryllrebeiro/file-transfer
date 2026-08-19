package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type Metadata struct {
	FileName  string `json:"fileName"`
	FileSize  int64  `json:"fileSize"`
	MimeType  string `json:"mimeType"`
	ChunkSize int    `json:"chunkSize"`
	SHA256    string `json:"sha256,omitempty"`
}
type Peer interface {
	SendControl([]byte) error
	SendBinary([]byte) error
	Close() error
}
type Session struct {
	ID                string    `json:"id"`
	Metadata          Metadata  `json:"metadata"`
	CreatedAt         time.Time `json:"createdAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	State             State     `json:"state"`
	SenderConnected   bool      `json:"senderConnected"`
	ReceiverConnected bool      `json:"receiverConnected"`
	Accepted          bool      `json:"accepted"`
	Sender            Peer      `json:"-"`
	Receiver          Peer      `json:"-"`
	NextChunk         uint64    `json:"-"`
	LastChunkIndex    uint64    `json:"-"`
	LastChunk         []byte    `json:"-"`
	Mu                sync.Mutex
}

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
func (session *Session) expired(now time.Time) bool { return now.After(session.ExpiresAt) }
func (session *Session) HasLastChunk() bool         { return len(session.LastChunk) > 0 }
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
	session.LastChunk = nil
	session.closeConnections()
	return true
}
func (session *Session) Snapshot() Session {
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return Session{ID: session.ID, Metadata: session.Metadata, CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt, State: session.State, SenderConnected: session.SenderConnected, ReceiverConnected: session.ReceiverConnected, Accepted: session.Accepted}
}

var errInvalidState = errors.New("invalid transfer state")
