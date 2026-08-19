package transfer

import (
	"bytes"
	"errors"
	"sync"
	"time"
)

type Role string

const (
	SenderRole   Role = "sender"
	ReceiverRole Role = "receiver"
)

var (
	ErrTransferNotFound = errors.New("transfer not found")
	ErrTransferExpired  = errors.New("transfer expired")
	ErrRoleConnected    = errors.New("role already connected")
	ErrInvalidRole      = errors.New("invalid role")
	ErrInvalidState     = errors.New("invalid transfer state")
)

type Manager struct {
	sessions     map[string]*Session
	mu           sync.RWMutex
	ttl          time.Duration
	maxFileSize  int64
	maxChunkSize int
}

func NewManager(ttl time.Duration, maxFileSize int64, maxChunkSize int) *Manager {
	return &Manager{sessions: make(map[string]*Session), ttl: ttl, maxFileSize: maxFileSize, maxChunkSize: maxChunkSize}
}

func (manager *Manager) Create(metadata Metadata) (*Session, error) {
	if metadata.FileSize <= 0 || metadata.FileSize > manager.maxFileSize {
		return nil, errors.New("file size exceeds limit")
	}
	if metadata.ChunkSize <= 0 || metadata.ChunkSize > manager.maxChunkSize {
		metadata.ChunkSize = manager.maxChunkSize
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &Session{ID: id, Metadata: metadata, CreatedAt: now, ExpiresAt: now.Add(manager.ttl), State: WaitingForReceiver}
	manager.mu.Lock()
	manager.sessions[id] = session
	manager.mu.Unlock()
	return session, nil
}

func (manager *Manager) Get(id string) (*Session, bool) {
	manager.mu.RLock()
	session, ok := manager.sessions[id]
	manager.mu.RUnlock()
	return session, ok
}

func (manager *Manager) getActive(id string) (*Session, error) {
	session, ok := manager.Get(id)
	if !ok {
		return nil, ErrTransferNotFound
	}
	if session.Expire(time.Now()) {
		return nil, ErrTransferExpired
	}
	snapshot := session.Snapshot()
	if IsTerminal(snapshot.State) {
		return nil, ErrInvalidState
	}
	return session, nil
}

func (manager *Manager) AttachSender(id string, connection Peer) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.SenderConnected {
		return nil, ErrRoleConnected
	}
	session.Sender = connection
	session.SenderConnected = true
	if session.ReceiverConnected {
		next := WaitingForAccept
		if session.Accepted && session.State == Paused {
			next = Transferring
		}
		if session.State == WaitingForReceiver || session.State == Paused {
			if err := transition(&session.State, next); err != nil {
				return nil, err
			}
		}
	}
	return session, nil
}

func (manager *Manager) AttachReceiver(id string, connection Peer) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.ReceiverConnected {
		return nil, ErrRoleConnected
	}
	session.Receiver = connection
	session.ReceiverConnected = true
	if session.SenderConnected {
		next := WaitingForAccept
		if session.Accepted && session.State == Paused {
			next = Transferring
		}
		if session.State == WaitingForReceiver || session.State == Paused {
			if err := transition(&session.State, next); err != nil {
				return nil, err
			}
		}
	}
	return session, nil
}

func (manager *Manager) Accept(id string) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if !session.SenderConnected || !session.ReceiverConnected || session.State != WaitingForAccept {
		return nil, ErrInvalidState
	}
	if err := transition(&session.State, Transferring); err != nil {
		return nil, err
	}
	session.Accepted = true
	return session, nil
}

func (manager *Manager) Complete(id string) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State != Transferring {
		return nil, ErrInvalidState
	}
	if err := transition(&session.State, Completing); err != nil {
		return nil, err
	}
	if err := transition(&session.State, Completed); err != nil {
		return nil, err
	}
	session.LastChunk = nil
	return session, nil
}

func (manager *Manager) Cancel(id string) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if IsTerminal(session.State) {
		return nil, ErrInvalidState
	}
	if err := transition(&session.State, Cancelled); err != nil {
		return nil, err
	}
	session.LastChunk = nil
	session.closeConnections()
	return session, nil
}

func (manager *Manager) Detach(id string, role Role, connection Peer) (*Session, bool) {
	session, ok := manager.Get(id)
	if !ok {
		return nil, false
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	detached := false
	switch role {
	case SenderRole:
		if session.Sender == connection {
			session.Sender = nil
			session.SenderConnected = false
			detached = true
		}
	case ReceiverRole:
		if session.Receiver == connection {
			session.Receiver = nil
			session.ReceiverConnected = false
			detached = true
		}
	}
	if detached && session.State == Transferring {
		_ = transition(&session.State, Paused)
	}
	return session, detached
}

func (manager *Manager) Connections(id string) (sender, receiver Peer, ok bool) {
	session, found := manager.Get(id)
	if !found {
		return nil, nil, false
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return session.Sender, session.Receiver, true
}

func (manager *Manager) PrepareChunk(id string, index uint64, frame []byte) (receiver, sender Peer, duplicate bool, err error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, nil, false, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State != Transferring || session.Receiver == nil || index != session.NextChunk || len(frame)-16 > session.Metadata.ChunkSize {
		if session.State == Transferring && session.Receiver != nil && session.HasLastChunk() && bytes.Equal(frame, session.LastChunk) {
			return session.Receiver, session.Sender, true, nil
		}
		return nil, nil, false, ErrInvalidState
	}
	session.NextChunk++
	return session.Receiver, session.Sender, false, nil
}

func (manager *Manager) RecordChunk(id string, index uint64, frame []byte) {
	session, ok := manager.Get(id)
	if !ok {
		return
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State == Transferring && index+1 == session.NextChunk {
		session.LastChunkIndex = index
		session.LastChunk = append(session.LastChunk[:0], frame...)
	}
}

func (manager *Manager) Delete(id string) {
	manager.mu.Lock()
	session, ok := manager.sessions[id]
	if ok {
		delete(manager.sessions, id)
	}
	manager.mu.Unlock()
	if ok {
		session.closeConnections()
	}
}

func (manager *Manager) Cleanup(now time.Time) int {
	manager.mu.RLock()
	sessions := make([]*Session, 0, len(manager.sessions))
	for _, session := range manager.sessions {
		sessions = append(sessions, session)
	}
	manager.mu.RUnlock()

	removed := 0
	for _, session := range sessions {
		if !session.Expire(now) {
			continue
		}
		manager.Delete(session.ID)
		removed++
	}
	return removed
}
