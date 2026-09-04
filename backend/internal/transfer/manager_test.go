package transfer

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type testPeer struct{}

func (testPeer) SendControl([]byte) error { return nil }
func (testPeer) SendBinary([]byte) error  { return nil }
func (testPeer) Close() error             { return nil }

func TestCreateUsesRandomIDAndExpires(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	first, err := manager.Create(Metadata{FileName: "a", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(Metadata{FileName: "b", FileSize: 10, ChunkSize: 5})
	if err != nil || first.ID == second.ID {
		t.Fatal("expected unique IDs")
	}
	if manager.Cleanup(time.Now().Add(2*time.Minute)) != 2 {
		t.Fatal("expected cleanup")
	}
}
func TestChunkFrame(t *testing.T) {
	frame := EncodeChunk(3, []byte("hello"))
	index, payload, ok := DecodeChunk(frame)
	if !ok || index != 3 || string(payload) != "hello" {
		t.Fatal("invalid frame")
	}
	frame[15] = 1
	if _, _, ok = DecodeChunk(frame); ok {
		t.Fatal("accepted malformed frame")
	}
}
func TestStateTransitionsRejectInvalidMoves(t *testing.T) {
	state := Created
	if err := transition(&state, WaitingForReceiver); err != nil {
		t.Fatal(err)
	}
	if err := transition(&state, Completed); err == nil {
		t.Fatal("expected invalid transition")
	}
	if state != WaitingForReceiver {
		t.Fatalf("state changed after rejected transition: %s", state)
	}
}
func TestManagerAcceptsOnlyAfterBothRolesAttach(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, err := manager.Create(Metadata{FileName: "a", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Accept(session.ID); err == nil {
		t.Fatal("expected accept to fail before connections")
	}
	if _, err = manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AttachReceiver(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	accepted, err := manager.Accept(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Snapshot().State != Transferring {
		t.Fatalf("expected transferring, got %s", accepted.Snapshot().State)
	}
}
func TestManagerDetachAllowsReconnect(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, err := manager.Create(Metadata{FileName: "a", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, detached := manager.Detach(session.ID, SenderRole, nil); !detached {
		t.Fatal("expected sender detach")
	}
	if _, err = manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
}
func TestManagerResumesPausedTransferAfterReconnect(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, err := manager.Create(Metadata{FileName: "a", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AttachReceiver(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Accept(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, detached := manager.Detach(session.ID, SenderRole, nil); !detached {
		t.Fatal("expected sender detach")
	}
	if session.Snapshot().State != Paused {
		t.Fatalf("expected paused state, got %s", session.Snapshot().State)
	}
	if _, err = manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().State != Transferring {
		t.Fatalf("expected resumed transfer, got %s", session.Snapshot().State)
	}
}

func TestManagerAcceptsOutOfOrderWithinWindowAndRetransmitsPastFloor(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, err := manager.Create(Metadata{FileName: "a", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	peer := testPeer{}
	if _, err = manager.AttachSender(session.ID, peer); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AttachReceiver(session.ID, peer); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Accept(session.ID); err != nil {
		t.Fatal(err)
	}
	// Chunk 3 admitted before chunks 0-2 (out of order within the parallel window).
	receiver, sender, err := manager.PrepareChunk(session.ID, 3, EncodeChunk(3, []byte("hello")))
	if err != nil || receiver == nil || sender == nil {
		t.Fatal("expected out-of-order admission within window")
	}
	if _, _, err = manager.PrepareChunk(session.ID, 0, EncodeChunk(0, []byte("zero"))); err != nil {
		t.Fatal(err)
	}
	next, err := manager.NextChunk(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next != 1 {
		t.Fatalf("expected contiguous floor to advance to 1, got %d", next)
	}
	// Retransmission of an already-contiguous chunk (index < floor) is re-forwarded, not rejected.
	if _, _, err = manager.PrepareChunk(session.ID, 0, EncodeChunk(0, []byte("zero"))); err != nil {
		t.Fatal("expected retransmission past floor to be admitted")
	}
	// Far-future indices beyond the parallel window are rejected.
	if _, _, err = manager.PrepareChunk(session.ID, ParallelWindow+5, EncodeChunk(ParallelWindow+5, []byte("far"))); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected far-future index rejection, got %v", err)
	}
	// Oversized frames are rejected.
	if _, _, err = manager.PrepareChunk(session.ID, 1, EncodeChunk(1, make([]byte, 64))); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected oversized frame rejection, got %v", err)
	}
}

func TestManagerEnforcesActiveTransferAndConnectionLimits(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	manager.SetLimits(1, 1)
	if _, err := manager.Create(Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(Metadata{FileName: "b.txt", FileSize: 10, ChunkSize: 5}); err == nil {
		t.Fatal("expected active transfer limit")
	}
	if !manager.AcquireConnection() {
		t.Fatal("expected first connection")
	}
	if manager.AcquireConnection() {
		t.Fatal("expected connection limit")
	}
	manager.ReleaseConnection()
	if !manager.AcquireConnection() {
		t.Fatal("expected released connection")
	}
}

func TestManagerAdjustChunkSizeClampsAndRequiresTransferring(t *testing.T) {
	manager := NewManager(time.Minute, 100, 1024*1024)
	session, err := manager.Create(Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.AdjustChunkSize(session.ID, 6); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected rejection before transferring, got %v", err)
	}
	if _, err := manager.AttachSender(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachReceiver(session.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Accept(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.AdjustChunkSize(session.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot().Metadata.ChunkSize; got != MinChunkSize {
		t.Fatalf("expected clamp to MinChunkSize, got %d", got)
	}
	if err := manager.AdjustChunkSize(session.ID, manager.maxChunkSize+100); err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot().Metadata.ChunkSize; got != manager.maxChunkSize {
		t.Fatalf("expected clamp to maxChunkSize, got %d", got)
	}
}

func TestManagerRejectsUnsafeMetadata(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	invalid := []Metadata{{FileName: "../secret", FileSize: 10}, {FileName: "file", FileSize: 10, SHA256: "not-a-hash"}}
	for _, metadata := range invalid {
		if _, err := manager.Create(metadata); err == nil {
			t.Fatal("expected metadata validation failure")
		}
	}
}

func TestManagerMetrics(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	if _, err := manager.Create(Metadata{FileName: "a.txt", FileSize: 10}); err != nil {
		t.Fatal(err)
	}
	manager.RecordCompleted()
	manager.RecordCancelled()
	manager.RecordFailed()
	manager.RecordBytesRelayed(12)
	manager.RecordQueueSaturation()
	metrics := manager.Metrics()
	if metrics.CreatedTransfers != 1 || metrics.CompletedTransfers != 1 || metrics.CancelledTransfers != 1 || metrics.FailedTransfers != 1 || metrics.BytesRelayed != 12 || metrics.QueueSaturated != 1 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestManagerShutdownClosesAndRemovesSessions(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, err := manager.Create(Metadata{FileName: "a.txt", FileSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	manager.Shutdown()
	if _, ok := manager.Get(session.ID); ok {
		t.Fatal("expected shutdown to remove sessions")
	}
	if metrics := manager.Metrics(); metrics.ActiveTransfers != 0 || metrics.ActiveConnections != 0 {
		t.Fatalf("expected empty manager after shutdown: %+v", metrics)
	}
}

func TestCapabilityTokensAreRoleScopedAndNotInSnapshots(t *testing.T) {
	manager := NewManager(time.Minute, 100, 10)
	session, tokens, err := manager.CreateWithTokens(Metadata{FileName: "secure.txt", FileSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.SenderToken == "" || tokens.ReceiverToken == "" || tokens.SenderToken == tokens.ReceiverToken {
		t.Fatal("expected distinct capability tokens")
	}
	if err := manager.ValidateToken(session.ID, SenderRole, tokens.SenderToken); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidateToken(session.ID, ReceiverRole, tokens.ReceiverToken); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidateToken(session.ID, SenderRole, tokens.ReceiverToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected sender role rejection, got %v", err)
	}
	if err := manager.ValidateToken(session.ID, ReceiverRole, "invalid"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token rejection, got %v", err)
	}
	snapshot := session.Snapshot()
	if snapshot.SenderTokenHash != [32]byte{} || snapshot.ReceiverTokenHash != [32]byte{} {
		t.Fatal("token hashes leaked into snapshot")
	}
}

func TestManagerConcurrentChurnAcrossShards(t *testing.T) {
	manager := NewManager(time.Minute, 100, 1024*1024)
	manager.SetLimits(10000, 10000)
	var wg sync.WaitGroup
	for worker := 0; worker < 64; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 25; iteration++ {
				session, err := manager.Create(Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
				if err != nil {
					continue
				}
				_, _ = manager.Get(session.ID)
				_ = manager.ValidateToken(session.ID, SenderRole, "bogus")
				_, _ = manager.Cancel(session.ID)
				manager.Delete(session.ID)
			}
		}()
	}
	wg.Wait()
	if metrics := manager.Metrics(); metrics.ActiveTransfers != 0 {
		t.Fatalf("expected all transfers reaped, got %d", metrics.ActiveTransfers)
	}
}
