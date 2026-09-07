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

type EncryptionMetadata struct {
	Scheme     string `json:"scheme"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Nonce      string `json:"nonce"`
}

type FileEntry struct {
	Name                string             `json:"name"`
	Size                int64              `json:"size"`
	MimeType            string             `json:"mimeType"`
	SHA256              string             `json:"sha256,omitempty"`
	SHA256Ciphertext    string             `json:"sha256Ciphertext,omitempty"`
	Offset              int64              `json:"offset,omitempty"`
}

type Metadata struct {
	FileName            string             `json:"fileName,omitempty"`
	FileSize            int64              `json:"fileSize,omitempty"`
	MimeType            string             `json:"mimeType,omitempty"`
	ChunkSize           int                `json:"chunkSize"`
	SHA256              string             `json:"sha256,omitempty"`
	Transport           string             `json:"transport,omitempty"`
	Features            []string           `json:"features,omitempty"`
	Encryption          *EncryptionMetadata `json:"encryption,omitempty"`
	SHA256Ciphertext    string             `json:"sha256Ciphertext,omitempty"`
	Files               []FileEntry        `json:"files,omitempty"`
	TotalSize           int64              `json:"totalSize,omitempty"`
	ManifestSHA256      string             `json:"manifestSha256,omitempty"`
	ManifestSHA256Cipher string            `json:"manifestSha256Ciphertext,omitempty"`
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
	parallelEnabled   bool
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
	return Session{
		ID: session.ID, 
		Metadata: session.Metadata, 
		CreatedAt: session.CreatedAt, 
		ExpiresAt: session.ExpiresAt, 
		State: session.State, 
		SenderConnected: session.SenderConnected, 
		ReceiverConnected: session.ReceiverConnected, 
		Accepted: session.Accepted, 
		TransportMode: session.TransportMode, 
		ActiveTransport: session.ActiveTransport,
		parallelEnabled: session.parallelEnabled,
	}
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
		// Enable parallel mode if both peers support it
		for _, f := range session.Metadata.Features {
			if f == "parallel-v1" {
				session.parallelEnabled = true
				break
			}
		}
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

func (session *Session) IsParallelEnabled() bool {
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return session.parallelEnabled
}

var errInvalidState = errors.New("invalid transfer state")
