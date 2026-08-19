package transfer

import (
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

func TestManagerAllowsExactLastChunkRetransmission(t *testing.T) {
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
	frame := EncodeChunk(0, []byte("hello"))
	receiver, sender, duplicate, err := manager.PrepareChunk(session.ID, 0, frame)
	if err != nil || receiver == nil || sender == nil || duplicate {
		t.Fatal("expected first chunk admission")
	}
	manager.RecordChunk(session.ID, 0, frame)
	_, _, duplicate, err = manager.PrepareChunk(session.ID, 0, frame)
	if err != nil || !duplicate {
		t.Fatal("expected exact duplicate admission")
	}
	altered := EncodeChunk(0, []byte("world"))
	if _, _, duplicate, err = manager.PrepareChunk(session.ID, 0, altered); err == nil || duplicate {
		t.Fatal("rejected altered duplicate")
	}
}
