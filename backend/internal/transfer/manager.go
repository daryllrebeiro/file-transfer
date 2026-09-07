package transfer

import (
	"context"
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

const managerShards = 16

type sessionShard struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

type Manager struct {
	shards             [managerShards]sessionShard
	mu                 sync.RWMutex
	ttl                time.Duration
	maxFileSize        int64
	maxChunkSize       int
	maxActiveTransfers int
	maxConnections     int
	connections        int
	transferCount      int
	created            atomic.Uint64
	completed          atomic.Uint64
	cancelled          atomic.Uint64
	expired            atomic.Uint64
	failed             atomic.Uint64
	bytesRelayed       atomic.Uint64
	queueSaturated     atomic.Uint64
	redisStore         *RedisStore
	draining           atomic.Bool
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

func NewManager(ttl time.Duration, maxFileSize int64, maxChunkSize int, redisStore ...*RedisStore) *Manager {
	var rs *RedisStore
	if len(redisStore) > 0 {
		rs = redisStore[0]
	}
	manager := &Manager{ttl: ttl, maxFileSize: maxFileSize, maxChunkSize: maxChunkSize, maxActiveTransfers: 1000, maxConnections: 2000, redisStore: rs}
	for index := range manager.shards {
		manager.shards[index].sessions = make(map[string]*Session)
	}
	return manager
}

func (manager *Manager) shardFor(id string) int {
	var hash uint32
	for index := 0; index < len(id); index++ {
		hash = hash*31 + uint32(id[index])
	}
	return int(hash % managerShards)
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
	// Support both single-file (legacy) and multi-file modes
	isMultiFile := len(metadata.Files) > 0
	
	if isMultiFile {
		// Multi-file validation
		if metadata.TotalSize <= 0 || metadata.TotalSize > manager.maxFileSize {
			return nil, TokenPair{}, ErrInvalidFileSize
		}
		for _, f := range metadata.Files {
			if strings.TrimSpace(f.Name) == "" || len(f.Name) > 255 || strings.ContainsAny(f.Name, "\\/\x00\r\n") {
				return nil, TokenPair{}, ErrInvalidFileName
			}
			if f.Size <= 0 {
				return nil, TokenPair{}, ErrInvalidFileSize
			}
		}
	} else {
		// Legacy single-file validation
		if strings.TrimSpace(metadata.FileName) == "" || len(metadata.FileName) > 255 || strings.ContainsAny(metadata.FileName, "\\/\x00\r\n") {
			return nil, TokenPair{}, ErrInvalidFileName
		}
		if len(metadata.MimeType) > 128 || strings.ContainsAny(metadata.MimeType, "\r\n") {
			return nil, TokenPair{}, ErrInvalidMIME
		}
	}
	
	// Validate encryption metadata
	if metadata.Encryption != nil {
		if metadata.Encryption.Scheme != "aes-gcm-pbkdf2" {
			return nil, TokenPair{}, ErrInvalidSHA256
		}
		if metadata.Encryption.Iterations != 250000 {
			return nil, TokenPair{}, ErrInvalidSHA256
		}
		// When encryption is used, sha256Ciphertext is required, sha256 (plaintext) is forbidden
		if isMultiFile {
			if metadata.ManifestSHA256Cipher == "" || len(metadata.ManifestSHA256Cipher) != 64 || strings.Trim(metadata.ManifestSHA256Cipher, "0123456789abcdefABCDEF") != "" {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
			if metadata.ManifestSHA256 != "" {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
		} else {
			if metadata.SHA256Ciphertext == "" || len(metadata.SHA256Ciphertext) != 64 || strings.Trim(metadata.SHA256Ciphertext, "0123456789abcdefABCDEF") != "" {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
			if metadata.SHA256 != "" {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
		}
	} else {
		// No encryption: sha256 is optional but if present must be valid
		if isMultiFile {
			if metadata.ManifestSHA256 != "" && (len(metadata.ManifestSHA256) != 64 || strings.Trim(metadata.ManifestSHA256, "0123456789abcdefABCDEF") != "") {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
		} else {
			if metadata.SHA256 != "" && (len(metadata.SHA256) != 64 || strings.Trim(metadata.SHA256, "0123456789abcdefABCDEF") != "") {
				return nil, TokenPair{}, ErrInvalidSHA256
			}
		}
	}
	
	// Validate file size
	if isMultiFile {
		if metadata.TotalSize <= 0 || metadata.TotalSize > manager.maxFileSize {
			return nil, TokenPair{}, ErrInvalidFileSize
		}
	} else {
		if metadata.FileSize <= 0 || metadata.FileSize > manager.maxFileSize {
			return nil, TokenPair{}, ErrInvalidFileSize
		}
	}
	if metadata.ChunkSize <= 0 || metadata.ChunkSize > manager.maxChunkSize {
		metadata.ChunkSize = manager.maxChunkSize
	}
	id, err := newID()
	if err != nil {
		return nil, TokenPair{}, err
	}
	var pair TokenPair
	if withTokens {
		pair, err = newTokenPair()
		if err != nil {
			return nil, TokenPair{}, err
		}
	}
	now := time.Now()
	manager.mu.Lock()
	if manager.maxActiveTransfers > 0 && manager.transferCount >= manager.maxActiveTransfers {
		manager.mu.Unlock()
		return nil, TokenPair{}, ErrTransferLimit
	}
	manager.transferCount++
	manager.mu.Unlock()
	session := &Session{
		ID:              id,
		Metadata:        metadata,
		CreatedAt:       now,
		ExpiresAt:       now.Add(manager.ttl),
		State:           WaitingForReceiver,
		TransportMode:   TransportMode(metadata.Transport),
		ActiveTransport: TransportMode(metadata.Transport),
	}
	if withTokens {
		session.SenderTokenHash = hashToken(pair.SenderToken)
		session.ReceiverTokenHash = hashToken(pair.ReceiverToken)
	}
shard := &manager.shards[manager.shardFor(id)]
	shard.mu.Lock()
	shard.sessions[id] = session
shard.mu.Unlock()
	manager.created.Add(1)

	// Persist to Redis if configured
	if manager.redisStore != nil {
		go func() {
			if err := manager.redisStore.SaveSession(id, string(WaitingForReceiver), string(TransportMode(metadata.Transport)), time.Now().Unix(), time.Now().Add(manager.ttl).Unix()); err != nil {
				manager.redisStore.logger.Error("Failed to save session to Redis", "error", err, "id", id)
			}
		}()
	}
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
	shard := &manager.shards[manager.shardFor(id)]
	shard.mu.RLock()
	session, ok := shard.sessions[id]
	shard.mu.RUnlock()
	if !ok {
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
		return ErrInvalidRole
	}
	if expected != [32]byte{} && subtle.ConstantTimeCompare(expected[:], presented[:]) == 1 {
		return nil
	}
	return ErrInvalidToken
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
	activeTransfers := 0
	for index := range manager.shards {
		shard := &manager.shards[index]
		shard.mu.RLock()
		activeTransfers += len(shard.sessions)
		shard.mu.RUnlock()
	}
	manager.mu.RLock()
	activeConnections := manager.connections
	manager.mu.RUnlock()
	return Metrics{ActiveTransfers: activeTransfers, ActiveConnections: activeConnections, CreatedTransfers: manager.created.Load(), CompletedTransfers: manager.completed.Load(), CancelledTransfers: manager.cancelled.Load(), ExpiredTransfers: manager.expired.Load(), FailedTransfers: manager.failed.Load(), BytesRelayed: manager.bytesRelayed.Load(), QueueSaturated: manager.queueSaturated.Load()}
}

func (manager *Manager) SetDraining(draining bool) {
	manager.draining.Store(draining)
}

func (manager *Manager) IsDraining() bool {
	return manager.draining.Load()
}

func (manager *Manager) ActiveTransferCount() int {
	count := 0
	for index := range manager.shards {
		shard := &manager.shards[index]
		shard.mu.RLock()
		count += len(shard.sessions)
		shard.mu.RUnlock()
	}
	return count
}

func (manager *Manager) WaitForDrain(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if manager.ActiveTransferCount() == 0 {
				return nil
			}
		}
	}
}

func (manager *Manager) Get(id string) (*Session, bool) {
	shard := &manager.shards[manager.shardFor(id)]
	shard.mu.RLock()
	session, ok := shard.sessions[id]
	shard.mu.RUnlock()
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

func (manager *Manager) Pause(id string) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if err := transition(&session.State, Paused); err != nil {
		return nil, err
	}
	return session, nil
}

func (manager *Manager) Rewind(id string, fromChunk uint64) (*Session, error) {
	session, err := manager.getActive(id)
	if err != nil {
		return nil, err
	}
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.State != Transferring && session.State != Paused {
		return nil, ErrInvalidState
	}
	if fromChunk < session.NextChunk {
		session.NextChunk = fromChunk
	}
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
	shard := &manager.shards[manager.shardFor(id)]
	shard.mu.Lock()
	session, ok := shard.sessions[id]
	if ok {
		delete(shard.sessions, id)
	}
	shard.mu.Unlock()
	if ok {
		manager.mu.Lock()
		if manager.transferCount > 0 {
			manager.transferCount--
		}
		manager.mu.Unlock()
		session.closeConnections()
	}
}

func (manager *Manager) Shutdown() {
	var sessions []*Session
	for index := range manager.shards {
		shard := &manager.shards[index]
		shard.mu.Lock()
		for id, session := range shard.sessions {
			delete(shard.sessions, id)
			sessions = append(sessions, session)
		}
		shard.mu.Unlock()
	}
	manager.mu.Lock()
	manager.connections = 0
	manager.transferCount = 0
	manager.mu.Unlock()
	for _, session := range sessions {
		session.closeConnections()
	}
}

func (manager *Manager) Cleanup(now time.Time) int {
	removed := 0
	for index := range manager.shards {
		shard := &manager.shards[index]
		shard.mu.RLock()
		snapshot := make([]*Session, 0, len(shard.sessions))
		for _, session := range shard.sessions {
			snapshot = append(snapshot, session)
		}
		shard.mu.RUnlock()
		for _, session := range snapshot {
			if !session.Expire(now) {
				continue
			}
			shard.mu.Lock()
			if current, ok := shard.sessions[session.ID]; ok && current == session {
				delete(shard.sessions, session.ID)
			}
			shard.mu.Unlock()
			manager.mu.Lock()
			if manager.transferCount > 0 {
				manager.transferCount--
			}
			manager.mu.Unlock()
			manager.RecordExpired()
			removed++
		}
	}
	return removed
}
