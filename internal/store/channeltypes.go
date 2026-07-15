package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openstream/openstream/internal/domain"
)

// EnsureBuiltinChannelTypes seeds the five built-in types for an app
// (SPEC.md §6.2); existing rows are left untouched.
func EnsureBuiltinChannelTypes(ctx context.Context, q Querier, appID string) error {
	for name, cfg := range domain.BuiltinChannelTypes() {
		raw, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("store: marshal channel type: %w", err)
		}
		_, err = q.Exec(ctx, `
			INSERT INTO channel_types (app_id, name, builtin, config)
			VALUES ($1, $2, true, $3)
			ON CONFLICT (app_id, name) DO NOTHING`, appID, name, raw)
		if err != nil {
			return fmt.Errorf("store: seed channel type %s: %w", name, err)
		}
	}
	return nil
}

// EnsureBuiltinRoles seeds the built-in global roles (SPEC.md §7.1).
func EnsureBuiltinRoles(ctx context.Context, q Querier, appID string) error {
	for _, name := range []string{domain.RoleUser, domain.RoleGuest, domain.RoleAnonymous, domain.RoleAdmin, domain.RoleModerator} {
		_, err := q.Exec(ctx, `
			INSERT INTO roles (app_id, name, builtin) VALUES ($1, $2, true)
			ON CONFLICT (app_id, name) DO NOTHING`, appID, name)
		if err != nil {
			return fmt.Errorf("store: seed role %s: %w", name, err)
		}
	}
	return nil
}

// GetChannelType loads one channel-type config.
func GetChannelType(ctx context.Context, q Querier, appID, name string) (*domain.ChannelTypeConfig, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT config FROM channel_types WHERE app_id=$1 AND name=$2`, appID, name).Scan(&raw)
	if err != nil {
		return nil, notFoundOr(err, "store: get channel type")
	}
	var cfg domain.ChannelTypeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("store: channel type config: %w", err)
	}
	cfg.Name = name
	return &cfg, nil
}

// ListChannelTypes returns every type for an app.
func ListChannelTypes(ctx context.Context, q Querier, appID string) ([]*domain.ChannelTypeConfig, error) {
	rows, err := q.Query(ctx, `SELECT name, config FROM channel_types WHERE app_id=$1 ORDER BY name`, appID)
	if err != nil {
		return nil, fmt.Errorf("store: list channel types: %w", err)
	}
	defer rows.Close()
	var out []*domain.ChannelTypeConfig
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var cfg domain.ChannelTypeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("store: channel type config: %w", err)
		}
		cfg.Name = name
		out = append(out, &cfg)
	}
	return out, rows.Err()
}

// CreateChannelType adds a custom type, enforcing the per-app limit
// (SPEC.md §5.2 C3).
func CreateChannelType(ctx context.Context, q Querier, appID string, cfg domain.ChannelTypeConfig) error {
	var count int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM channel_types WHERE app_id=$1`, appID).Scan(&count); err != nil {
		return fmt.Errorf("store: count channel types: %w", err)
	}
	if count >= domain.MaxCustomChannelTypes+5 { // 5 built-ins don't count against the limit
		return fmt.Errorf("store: channel type limit (%d) reached", domain.MaxCustomChannelTypes)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("store: marshal channel type: %w", err)
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO channel_types (app_id, name, builtin, config)
		VALUES ($1, $2, false, $3)
		ON CONFLICT (app_id, name) DO NOTHING`, appID, cfg.Name, raw)
	if err != nil {
		return fmt.Errorf("store: create channel type: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: channel type %q already exists", cfg.Name)
	}
	return nil
}

// UpdateChannelType replaces a type's config.
func UpdateChannelType(ctx context.Context, q Querier, appID string, cfg domain.ChannelTypeConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("store: marshal channel type: %w", err)
	}
	tag, err := q.Exec(ctx, `UPDATE channel_types SET config=$3, updated_at=now() WHERE app_id=$1 AND name=$2`,
		appID, cfg.Name, raw)
	if err != nil {
		return fmt.Errorf("store: update channel type: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteChannelType removes a custom (non-builtin) type.
func DeleteChannelType(ctx context.Context, q Querier, appID, name string) error {
	tag, err := q.Exec(ctx, `DELETE FROM channel_types WHERE app_id=$1 AND name=$2 AND NOT builtin`, appID, name)
	if err != nil {
		return fmt.Errorf("store: delete channel type: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
