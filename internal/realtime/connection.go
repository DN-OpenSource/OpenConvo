package realtime

import (
	"context"
	"time"

	"github.com/coder/websocket"
	"github.com/oklog/ulid/v2"

	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

// helloEvent builds the health.check hello with connection_id and the full
// own_user (unreads, mutes, devices) per SPEC.md §8.1.
func (h *Hub) helloEvent(ctx context.Context, c *conn) ([]byte, error) {
	own := &domain.OwnUser{User: *c.user}
	total, channels, err := store.UnreadSummary(ctx, h.Store.Pool, c.appID, c.user.ID)
	if err != nil {
		return nil, err
	}
	own.TotalUnreadCount = total
	own.UnreadChannels = len(channels)
	if own.Mutes, err = store.ListMutes(ctx, h.Store.Pool, c.appID, c.user.ID); err != nil {
		return nil, err
	}
	if own.ChannelMutes, err = store.ListChannelMutes(ctx, h.Store.Pool, c.appID, c.user.ID); err != nil {
		return nil, err
	}
	if own.Devices, err = store.ListDevices(ctx, h.Store.Pool, c.appID, c.user.ID); err != nil {
		return nil, err
	}
	e := &domain.Event{
		EventID:      ulid.Make().String(),
		Type:         domain.EventHealthCheck,
		ConnectionID: c.id,
		Me:           own,
		CreatedAt:    time.Now().UTC(),
	}
	return e.Encode(), nil
}

// writeLoop drains the outbound queue onto the socket.
func (h *Hub) writeLoop(ctx context.Context, c *conn) {
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.out:
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.sock.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				h.unregister(ctx, c, websocket.StatusAbnormalClosure, "write failed")
				return
			}
		}
	}
}

// heartbeatLoop pings every HeartbeatInterval; a peer that misses the
// DeadTimeout is evicted (SPEC.md §8.1).
func (h *Hub) heartbeatLoop(ctx context.Context, c *conn) {
	ticker := time.NewTicker(h.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, h.DeadTimeout-h.HeartbeatInterval)
			err := c.sock.Ping(pingCtx)
			cancel()
			if err != nil {
				h.unregister(ctx, c, websocket.StatusGoingAway, "heartbeat timeout")
				return
			}
			if err := h.State.TouchConnection(ctx, c.appID, c.user.ID, c.id); err != nil {
				h.Log.Warn("touch connection", "error", err)
			}
			// Periodic health.check keeps intermediaries from idling the
			// connection out.
			e := &domain.Event{
				EventID:      ulid.Make().String(),
				Type:         domain.EventHealthCheck,
				ConnectionID: c.id,
				CreatedAt:    time.Now().UTC(),
			}
			c.enqueue(e.Encode())
		}
	}
}

// readLoop consumes inbound frames. Data frames carry client events
// (currently only health.check echoes are expected); control frames are
// handled by the library. Returning tears the connection down.
func (h *Hub) readLoop(ctx context.Context, c *conn) {
	for {
		_, _, err := c.sock.Read(ctx)
		if err != nil {
			h.unregister(ctx, c, websocket.StatusNormalClosure, "closed")
			return
		}
	}
}
