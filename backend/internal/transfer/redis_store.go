package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

type RedisStore struct {
	client  *redis.Client
	context context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	logger  *slog.Logger
}

type redisSessionData struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	TransportMode string `json:"transportMode"`
	CreatedAt     int64  `json:"createdAt"`
	ExpiresAt     int64  `json:"expiresAt"`
}

func NewRedisStore(redisURL string, logger *slog.Logger) (*RedisStore, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := redis.NewClient(opt)

	// Test connection
	_, err = client.Ping(ctx).Result()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("redis connect: %w", err)
	}

	rs := &RedisStore{
		client:  client,
		context: ctx,
		cancel:  cancel,
		logger:  logger,
	}

	go rs.listenForMessages()
	return rs, nil
}

func (rs *RedisStore) listenForMessages() {
	ps := rs.client.Subscribe(rs.context, "transfer:sessions")
	defer ps.Close()

	ch := ps.Channel()
	for msg := range ch {
		var payload struct {
			Type string `json:"type"`
			ID   string `json:"id,omitempty"`
		}
		if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
			rs.logger.Warn("Failed to unmarshal session payload", "error", err)
			continue
		}
		rs.logger.Info("Session signal received", "type", payload.Type, "id", payload.ID)
	}
}

func (rs *RedisStore) SaveSession(id string, state string, transportMode string, createdAt, expiresAt int64) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	data := redisSessionData{
		ID:            id,
		State:         state,
		TransportMode: transportMode,
		CreatedAt:     createdAt,
		ExpiresAt:     expiresAt,
	}

	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	key := "transfer:session:" + id
	if err := rs.client.Set(rs.context, key, dataBytes, 0).Err(); err != nil {
		return fmt.Errorf("redis set session: %w", err)
	}

	return nil
}

func (rs *RedisStore) GetSession(id string) (*redisSessionData, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	key := "transfer:session:" + id
	data, err := rs.client.Get(rs.context, key).Result()
	if err != nil {
		return nil, err
	}

	var sess redisSessionData
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, err
	}

	return &sess, nil
}

func (rs *RedisStore) DeleteSession(id string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	key := "transfer:session:" + id
	return rs.client.Del(rs.context, key).Err()
}

func (rs *RedisStore) Close() error {
	rs.cancel()
	return rs.client.Close()
}