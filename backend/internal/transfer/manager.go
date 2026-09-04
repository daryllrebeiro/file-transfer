package transfer

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Role string

const (
	SenderRole   Role = "sender"
	ReceiverRole Role = "receiver"
)

// MinChunkSize is the floor for adaptive relay chunk sizing.
const MinChunkSize = 256 * 1024

var (
	ErrTransferNotFound = errors.New("transfer not found")
	ErrTransferExpired  = errors.New("transfer expired")
	ErrRoleConnected    = errors.New("role already connected")
	ErrInvalidRole      = errors.New("invalid role")
	ErrInvalidState     = errors.New("invalid transfer state")
	ErrInvalidToken     = errors.New("invalid capability token")
	ErrInvalidFileName  = errors.New("invalid file name")
	ErrInvalidMIME      = errors.New("invalid MIME type")
	ErrInvalidSHA256    = errors.New("invalid SHA-256")
	ErrInvalidFileSize  = errors.New("file size exceeds limit")
	ErrInvalidChunkSize = errors.New("invalid chunk size")
	ErrTransferLimit    = errors.New("active transfer limit reached")
)

type TokenPair struct {
	SenderToken   string
	ReceiverToken string
}

type Manager struct {
	sessions           map[string]*Session
	mu                 sync.RWMutex
	ttl                time.Duration
	maxFileSize        int64
	maxChunkSize       int
	maxActiveTransfers int
	maxConnections     int
	connections        int
	created            atomic.Uint64
	completed          atomic.Uint64
	cancelled          atomic.Uint64
	expired            atomic.Uint64
	failed             atomic.Uint64
	bytesRelayed       atomic.Uint64
	queueSaturated     atomic.Uint64
}

type Metrics struct {
	ActiveTransfers    int    `json:"activeTransfers"`
	ActiveConnections  int    `json:"activeConnections"`
	CreatedTransfers   uint64 `json:"createdTransfers"`
	CompletedTransfers uint64 `json:"completedTransfers"`
	CancelledTransfers uint64 `json:"cancelledTransfers"`
	ExpiredTransfers   uint64 `json:"expiredTransfers"`
	FailedTransfers    uint64 `json:"failedTransfers"`
	BytesRelayed       uint64 `json:"bytesRelayed"`
	QueueSaturated     uint64 `json:"queueSaturated"`
}

type Limits struct {
	MaxFileSize  int64 `json:"maxFileSize"`
	MaxChunkSize int   `json:"maxChunkSize"`
}

func NewManager(ttl time.Duration, maxFileSize int64, maxChunkSize int) *Manager {
	return &Manager{sessions: make(map[string]*Session), ttl: ttl, maxFileSize: maxFileSize, maxChunkSize: maxChunkSize, maxActiveTransfers: 1000, maxConnections: 2000}
}

func (manager *Manager) SetLimits(maxActiveTransfers, maxConnections int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.maxActiveTransfers = maxActiveTransfers
	manager.maxConnections = maxConnections
}

func (manager *Manager) Limits() Limits {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return Limits{MaxFileSize: manager.maxFileSize, MaxChunkSize: manager.maxChunkSize}
}

func (manager *Manager) Create(metadata Metadata) (*Session, error) {
	session, _, err := manager.create(metadata, false)
	return session, err
}

func (manager *Manager) CreateWithTokens(metadata Metadata) (*Session, TokenPair, error) {
	return manager.create(metadata, true)
}

func (manager *Manager) create(metadata Metadata, withTokens bool) (*Session, TokenPair, error) {
	if strings.TrimSpace(metadata.FileName) == "" || len(metadata.FileName) > 255 || strings.ContainsAny(metadata.FileName, "\\/\x00\r\n") {
		return nil, TokenPair{}, ErrInvalidFileName
	}
	if len(metadata.MimeType) > 128 || strings.ContainsAny(metadata.MimeType, "\r\n") {
		return nil, TokenPair{}, ErrInvalidMIME
	}
	if metadata.SHA256 != "" && (len(metadata.SHA256) != 64 || strings.Trim(metadata.SHA256, "0123456789abcdefABCDEF") != "") {
		return nil, TokenPair{}, ErrInvalidSHA256
	}
	if metadata.FileSize <= 0 || metadata.FileSize > manager.maxFileSize {
		return nil, TokenPair{}, ErrInvalidFileSize
	}
	if metadata.ChunkSize <= 0 || metadata.ChunkSize > manager.maxChunkSize {
		metadata.ChunkSize = manager.maxChunkSize
	}
	id, err := newID()
	if err != nil {
		return nil, TokenPair{}, err
	}
	now := time.Now()
	manager.mu.Lock()
	if manager.maxActiveTransfers > 0 && len(manager.sessions) >= manager.maxActiveTransfers {
		manager.mu.Unlock()
		return nil, TokenPair{}, ErrTransferLimit
	}
	session := &Session{
		ID:              id,
		Metadata:        metadata,
		CreatedAt:       now,
		ExpiresAt:       now.Add(manager.ttl),
		State:           WaitingForReceiver,
		TransportMode:   TransportMode(metadata.Transport),
		ActiveTransport: TransportMode(metadata.Transport),
	}
	var pair TokenPair
	if withTokens {
		pair, err = newTokenPair()
		if err != nil {
			manager.mu.Unlock()
			return nil, TokenPair{}, err
		}
		session.SenderTokenHash = hashToken(pair.SenderToken)
		session.ReceiverTokenHash = hashToken(pair.ReceiverToken)
	}
	manager.sessions[id] = session
	manager.mu.Unlock()
	manager.created.Add(1)
	return session, pair, nil
}

func newTokenPair() (TokenPair, error) {
	sender, err := newToken()
	if err != nil {
		return TokenPair{}, err
	}
	receiver, err := newToken()
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{SenderToken: sender, ReceiverToken: receiver}, nil
}
func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func hashToken(token string) [32]byte { return sha256.Sum256([]byte(token)) }
func (manager *Manager) ValidateToken(id string, role Role, token string) error {
	manager.mu.RLock()
	session, ok := manager.sessions[id]
	if !ok {
		manager.mu.RUnlock()
		return ErrTransferNotFound
	}
	presented := hashToken(token)
	var expected [32]byte
	switch role {
	case SenderRole:
		expected = session.SenderTokenHash
	case ReceiverRole:
		expected = session.ReceiverTokenHash
	default:
		manager.mu.RUnlock()
		return ErrInvalidRole
	}
	valid := expected != [32]byte{} && subtle.ConstantTimeCompare(expected[:], presented[:]) == 1
	manager.mu.RUnlock()
	if !valid {
		return ErrInvalidToken
	}
	return nil
}

func (manager *Manager) NextChunk(id string) (uint64, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return 0, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	return session.NextChunk, nil
}

func (manager *Manager) AcquireConnection() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.maxConnections > 0 && manager.connections >= manager.maxConnections {
		return false
	}
	manager.connections++
	return true
}
func (manager *Manager) ReleaseConnection() {
	manager.mu.Lock()
	if manager.connections > 0 {
		manager.connections--
	}
	manager.mu.Unlock()
}

func (manager *Manager) RecordCompleted() { manager.completed.Add(1) }
func (manager *Manager) RecordCancelled() { manager.cancelled.Add(1) }
func (manager *Manager) RecordFailed()    { manager.failed.Add(1) }
func (manager *Manager) RecordBytesRelayed(count int) {
	if count > 0 {
		manager.bytesRelayed.Add(uint64(count))
	}
}
func (manager *Manager) RecordQueueSaturation() { manager.queueSaturated.Add(1) }
func (manager *Manager) RecordExpired()         { manager.expired.Add(1) }
func (manager *Manager) Metrics() Metrics {
	manager.mu.RLock()
	activeTransfers, activeConnections := len(manager.sessions), manager.connections
	manager.mu.RUnlock()
	return Metrics{ActiveTransfers: activeTransfers, ActiveConnections: activeConnections, CreatedTransfers: manager.created.Load(), CompletedTransfers: manager.completed.Load(), CancelledTransfers: manager.cancelled.Load(), ExpiredTransfers: manager.expired.Load(), FailedTransfers: manager.failed.Load(), BytesRelayed: manager.bytesRelayed.Load(), QueueSaturated: manager.queueSaturated.Load()}
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
	session.Sender = connection
	if err := session.OnRoleAttached(SenderRole); err != nil {
		return nil, err
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
	session.Receiver = connection
	if err := session.OnRoleAttached(ReceiverRole); err != nil {
		return nil, err
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
	session.accepted = nil
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
	session.accepted = nil
	return session, nil
}

func (manager *Manager) Extend(id string, now time.Time) (time.Time, error) {
	session, ok := manager.Get(id)
	if !ok {
		return time.Time{}, ErrTransferNotFound
	}
	if session.Expire(now) {
		return time.Time{}, ErrTransferExpired
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if IsTerminal(session.State) {
		return time.Time{}, ErrInvalidState
	}
	capTime := session.CreatedAt.Add(2 * manager.ttl)
	next := session.ExpiresAt.Add(manager.ttl)
	if next.After(capTime) {
		next = capTime
	}
	session.ExpiresAt = next
	return session.ExpiresAt, nil
}

func (manager *Manager) AdjustChunkSize(id string, requested int) error {
	session, err := manager.getActive(id)
	if err != nil {
		return err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State != Transferring {
		return ErrInvalidState
	}
	size := requested
	if size < MinChunkSize {
		size = MinChunkSize
	}
	if size > manager.maxChunkSize {
		size = manager.maxChunkSize
	}
	session.Metadata.ChunkSize = size
	return nil
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

func (manager *Manager) PrepareChunk(id string, index uint64, frame []byte) (receiver, sender Peer, err error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State != Transferring || session.Receiver == nil {
		return nil, nil, ErrInvalidState
	}
	if index < session.NextChunk {
		// Already past the contiguous floor: ACK-loss retransmission. Re-forward so the
		// receiver (which drops duplicates by index) and sender see consistent ACKs.
		return session.Receiver, session.Sender, nil
	}
	if len(frame)-16 > session.Metadata.ChunkSize {
		return nil, nil, ErrInvalidState
	}
	if index >= session.NextChunk+ParallelWindow {
		return nil, nil, ErrInvalidState
	}
	if session.accepted == nil {
		session.accepted = make(map[uint64]bool)
	}
	if !session.accepted[index] {
		session.accepted[index] = true
		for session.accepted[session.NextChunk] {
			delete(session.accepted, session.NextChunk)
			session.NextChunk++
		}
	}
	return session.Receiver, session.Sender, nil
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

func (manager *Manager) Shutdown() {
	manager.mu.Lock()
	sessions := make([]*Session, 0, len(manager.sessions))
	for id, session := range manager.sessions {
		delete(manager.sessions, id)
		sessions = append(sessions, session)
	}
	manager.connections = 0
	manager.mu.Unlock()
	for _, session := range sessions {
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
		manager.RecordExpired()
		removed++
	}
	return removed
}
