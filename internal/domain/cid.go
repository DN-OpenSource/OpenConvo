package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ErrInvalidCID is returned for malformed channel addresses.
var ErrInvalidCID = errors.New("invalid cid: expected \"type:id\"")

// channelPartRe restricts channel type and id charsets (SPEC.md §4.3).
// The "!" prefix is reserved for server-derived ids (distinct channels).
var (
	channelTypeRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	channelIDRe   = regexp.MustCompile(`^!?[a-zA-Z0-9!_=@-]{1,64}$`)
	userIDRe      = regexp.MustCompile(`^[a-zA-Z0-9@_.-]{1,64}$`)
)

// CID formats the universal channel address "{type}:{id}" (SPEC.md §4.3).
func CID(channelType, channelID string) string {
	return channelType + ":" + channelID
}

// ParseCID splits a "{type}:{id}" channel address.
func ParseCID(cid string) (channelType, channelID string, err error) {
	channelType, channelID, ok := strings.Cut(cid, ":")
	if !ok || channelType == "" || channelID == "" {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidCID, cid)
	}
	return channelType, channelID, nil
}

// ValidateChannelType reports whether s is a legal channel type name.
func ValidateChannelType(s string) error {
	if !channelTypeRe.MatchString(s) {
		return fmt.Errorf("invalid channel type %q: 1-64 chars of [a-zA-Z0-9_-]", s)
	}
	return nil
}

// ValidateChannelID reports whether s is a legal channel id.
func ValidateChannelID(s string) error {
	if !channelIDRe.MatchString(s) {
		return fmt.Errorf("invalid channel id %q: 1-64 chars of [a-zA-Z0-9_-]", s)
	}
	return nil
}

// ValidateUserID reports whether s is a legal user id.
func ValidateUserID(s string) error {
	if !userIDRe.MatchString(s) {
		return fmt.Errorf("invalid user id %q: 1-64 chars of [a-zA-Z0-9@_.-]", s)
	}
	return nil
}

// DistinctChannelID derives the deterministic id for a distinct channel
// (DM/group identified by its member set, SPEC.md §5.2 C4): the same member
// set always resolves to the same conversation.
func DistinctChannelID(memberIDs []string) string {
	ids := make([]string, 0, len(memberIDs))
	seen := make(map[string]struct{}, len(memberIDs))
	for _, id := range memberIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return "!members-" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// IsDistinctChannelID reports whether id was derived from a member set.
func IsDistinctChannelID(id string) bool {
	return strings.HasPrefix(id, "!members-")
}
