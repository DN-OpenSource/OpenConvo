// Package bus abstracts the event bus (SPEC.md §2.3, §8.3): NATS JetStream
// in clustered deployments, an in-process implementation for single-binary
// mode and tests. Subjects are evt.{app}.{channelType}.{channelID} for
// channel-scoped events and usr.{app}.{userID} for user-scoped events.
package bus

import (
	"context"
	"fmt"
	"strings"

	"github.com/openstream/openstream/internal/domain"
)

// Handler consumes one published event.
type Handler func(topic string, payload []byte)

// Subscription is an active subscription that can be cancelled.
type Subscription interface {
	Unsubscribe() error
}

// Bus publishes and subscribes to event topics. Pattern syntax follows
// NATS: "." separators, "*" single-token wildcard, ">" tail wildcard.
type Bus interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	// Subscribe delivers every matching event to handler. queue, when
	// non-empty, load-balances across subscribers in the same group
	// (worker semantics); empty fan-outs to all subscribers.
	Subscribe(pattern, queue string, handler Handler) (Subscription, error)
	Close() error
}

// ChannelTopic builds the channel-scoped subject for an event.
func ChannelTopic(appID, channelType, channelID string) string {
	return fmt.Sprintf("evt.%s.%s.%s", appID, channelType, sanitizeToken(channelID))
}

// UserTopic builds the user-scoped subject.
func UserTopic(appID, userID string) string {
	return fmt.Sprintf("usr.%s.%s", appID, sanitizeToken(userID))
}

// TopicFor picks the right subject for an event.
func TopicFor(appID string, e *domain.Event) string {
	if e.IsUserScoped() && e.User != nil {
		return UserTopic(appID, e.User.ID)
	}
	return ChannelTopic(appID, e.ChannelType, e.ChannelID)
}

// sanitizeToken keeps ids NATS-subject-safe: '.', '*', '>' and spaces are
// replaced. Channel/user id charsets already exclude these (domain
// validation), so this is defense in depth.
func sanitizeToken(s string) string {
	r := strings.NewReplacer(".", "_", "*", "_", ">", "_", " ", "_")
	return r.Replace(s)
}

// MatchPattern reports whether topic matches a NATS-style pattern; used by
// the in-process bus and tested against NATS semantics.
func MatchPattern(pattern, topic string) bool {
	pt := strings.Split(pattern, ".")
	tt := strings.Split(topic, ".")
	for i, p := range pt {
		if p == ">" {
			return i < len(tt)
		}
		if i >= len(tt) {
			return false
		}
		if p != "*" && p != tt[i] {
			return false
		}
	}
	return len(pt) == len(tt)
}
