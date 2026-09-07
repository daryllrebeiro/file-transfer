package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type SignalingMessage struct {
	TransferID string          `json:"transferId"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type RedisSignaling struct {
	client    *redis.Client
	pubsub    *redis.PubSub
	mu        sync.Mutex
	handlers  map[string][]func(SignalingMessage)
	ctx       context.Context
	cancel    context.CancelFunc
	logger    *slog.Logger
}

func NewRedisSignaling(redisURL string, logger *slog.Logger) (*RedisSignaling, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := redis.NewClient(opt)

	// Test connection
	ctx, cancelTimeout := context.WithTimeout(ctx, 5*time.Second)
	defer cancelTimeout()
	if err := client.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("redis connect: %w", err)
	}

	rs := &RedisSignaling{
		client:   client,
		ctx:      ctx,
		cancel:   cancel,
		handlers: make(map[string][]func(SignalingMessage)),
		logger:   logger,
	}

	// Subscribe to signaling channel
	rs.pubsub = client.Subscribe(ctx, "signaling")
	go rs.listen()

	return rs, nil
}

func (rs *RedisSignaling) listen() {
	ch := rs.pubsub.Channel()
	for {
		select {
		case <-rs.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var signalingMsg SignalingMessage
			if err := json.Unmarshal([]byte(msg.Payload), &signalingMsg); err != nil {
				rs.logger.Warn("Failed to unmarshal signaling message", "error", err)
				continue
			}
			rs.dispatch(signalingMsg)
		}
	}
}

func (rs *RedisSignaling) Publish(transferID, msgType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	msg := SignalingMessage{
		TransferID: transferID,
		Type:       msgType,
		Payload:    data,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	return rs.client.Publish(rs.ctx, "signaling", msgData).Err()
}

func (rs *RedisSignaling) Subscribe(transferID string, handler func(SignalingMessage)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	key := transferID
	rs.handlers[key] = append(rs.handlers[key], handler)
}

func (rs *RedisSignaling) Unsubscribe(transferID string, handler func(SignalingMessage)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	key := transferID
	handlers := rs.handlers[key]
	for i, h := range handlers {
		// Note: function comparison is not reliable in Go
		// In practice, you'd use a subscription ID
		_ = h
		_ = i
	}
	// For simplicity, we clear all handlers for this transfer
	delete(rs.handlers, key)
}

func (rs *RedisSignaling) dispatch(msg SignalingMessage) {
	rs.mu.Lock()
	handlers := rs.handlers[msg.TransferID]
	rs.mu.Unlock()

	for _, handler := range handlers {
		handler(msg)
	}
}

func (rs *RedisSignaling) Close() error {
	rs.cancel()
	if rs.pubsub != nil {
		rs.pubsub.Close()
	}
	return rs.client.Close()
}

// Pub/Sub message types for signaling
const (
	SignalTypeOffer       = "offer"
	SignalTypeAnswer      = "answer"
	SignalTypeIceCandidate = "ice_candidate"
	SignalTypeWebRTCFallback = "webrtc_fallback"
	SignalTypeTransferComplete = "transfer_complete"
	SignalTypeTransferCancelled = "transfer_cancelled"
	SignalTypeReceiverConnected = "receiver_connected"
	SignalTypeChunkSizeChange = "chunk_size_change"
	SignalTypePause       = "pause"
	SignalTypeRewind      = "rewind"
)