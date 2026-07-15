package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis implements State on Redis/Valkey for clustered deployments.
type Redis struct {
	client *redis.Client
	// connTTL bounds connection-registry entries so crashed nodes don't
	// leak presence (heartbeats refresh it).
	connTTL time.Duration
}

// NewRedis connects and pings.
func NewRedis(ctx context.Context, addr string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("state: redis ping: %w", err)
	}
	return &Redis{client: client, connTTL: 90 * time.Second}, nil
}

func (r *Redis) connKey(appID, userID string) string {
	return fmt.Sprintf("os:conn:%s:%s", appID, userID)
}

// AddConnection registers a connection in the user's hash; each field
// carries its own liveness deadline refreshed by heartbeats.
func (r *Redis) AddConnection(ctx context.Context, appID, userID, connID string) (bool, error) {
	k := r.connKey(appID, userID)
	count, err := r.liveConnCount(ctx, k)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(r.connTTL).UnixMilli()
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, k, connID, deadline)
	pipe.Expire(ctx, k, r.connTTL*2)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("state: add connection: %w", err)
	}
	return count == 0, nil
}

// RemoveConnection drops a connection.
func (r *Redis) RemoveConnection(ctx context.Context, appID, userID, connID string) (bool, error) {
	k := r.connKey(appID, userID)
	if err := r.client.HDel(ctx, k, connID).Err(); err != nil {
		return false, fmt.Errorf("state: remove connection: %w", err)
	}
	count, err := r.liveConnCount(ctx, k)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// TouchConnection refreshes the connection's liveness deadline.
func (r *Redis) TouchConnection(ctx context.Context, appID, userID, connID string) error {
	k := r.connKey(appID, userID)
	deadline := time.Now().Add(r.connTTL).UnixMilli()
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, k, connID, deadline)
	pipe.Expire(ctx, k, r.connTTL*2)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("state: touch connection: %w", err)
	}
	return nil
}

// liveConnCount counts fields with a future deadline, lazily pruning dead
// ones.
func (r *Redis) liveConnCount(ctx context.Context, key string) (int, error) {
	fields, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("state: read connections: %w", err)
	}
	now := time.Now().UnixMilli()
	live := 0
	var dead []string
	for connID, deadlineStr := range fields {
		var deadline int64
		_, scanErr := fmt.Sscanf(deadlineStr, "%d", &deadline)
		if scanErr != nil || deadline < now {
			dead = append(dead, connID)
			continue
		}
		live++
	}
	if len(dead) > 0 {
		_ = r.client.HDel(ctx, key, dead...).Err()
	}
	return live, nil
}

// OnlineUsers reports presence for a user set.
func (r *Redis) OnlineUsers(ctx context.Context, appID string, userIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		count, err := r.liveConnCount(ctx, r.connKey(appID, id))
		if err != nil {
			return nil, err
		}
		out[id] = count > 0
	}
	return out, nil
}

// AdjustWatchers moves a channel watcher count.
func (r *Redis) AdjustWatchers(ctx context.Context, appID, cid string, delta int) (int, error) {
	k := fmt.Sprintf("os:watch:%s:%s", appID, cid)
	n, err := r.client.IncrBy(ctx, k, int64(delta)).Result()
	if err != nil {
		return 0, fmt.Errorf("state: adjust watchers: %w", err)
	}
	if n < 0 {
		_ = r.client.Set(ctx, k, 0, 0).Err()
		n = 0
	}
	return int(n), nil
}

// WatcherCount reads a channel watcher count.
func (r *Redis) WatcherCount(ctx context.Context, appID, cid string) (int, error) {
	n, err := r.client.Get(ctx, fmt.Sprintf("os:watch:%s:%s", appID, cid)).Int()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("state: watcher count: %w", err)
	}
	return n, nil
}

// rateScript admits/increments atomically (fixed window).
var rateScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
local ttl = redis.call('PTTL', KEYS[1])
if current > tonumber(ARGV[1]) then
  return {0, 0, ttl}
end
return {1, tonumber(ARGV[1]) - current, ttl}
`)

// RateAllow implements a fixed-window limiter with a Lua script.
func (r *Redis) RateAllow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Time, error) {
	res, err := rateScript.Run(ctx, r.client, []string{"os:rate:" + key}, limit, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, time.Time{}, fmt.Errorf("state: rate allow: %w", err)
	}
	allowed := res[0].(int64) == 1
	remaining := int(res[1].(int64))
	ttlMs := res[2].(int64)
	reset := time.Now().Add(time.Duration(ttlMs) * time.Millisecond)
	return allowed, remaining, reset, nil
}

// Close closes the client.
func (r *Redis) Close() error { return r.client.Close() }
