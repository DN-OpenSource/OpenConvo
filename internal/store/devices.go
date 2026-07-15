package store

import (
	"context"
	"fmt"

	"github.com/openstream/openstream/internal/domain"
)

// UpsertDevice registers a push device (SPEC.md §5.3 U11).
func UpsertDevice(ctx context.Context, q Querier, appID string, d *domain.Device) error {
	_, err := q.Exec(ctx, `
		INSERT INTO devices (app_id, user_id, id, push_provider, push_provider_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (app_id, user_id, id) DO UPDATE SET
			push_provider=EXCLUDED.push_provider,
			push_provider_name=EXCLUDED.push_provider_name,
			disabled=false, disabled_reason=''`,
		appID, d.UserID, d.ID, d.PushProvider, d.PushProviderName)
	if err != nil {
		return fmt.Errorf("store: upsert device: %w", err)
	}
	return nil
}

// ListDevices returns a user's devices.
func ListDevices(ctx context.Context, q Querier, appID, userID string) ([]*domain.Device, error) {
	rows, err := q.Query(ctx, `SELECT id, push_provider, push_provider_name, user_id, disabled, disabled_reason, created_at
		FROM devices WHERE app_id=$1 AND user_id=$2 ORDER BY created_at`, appID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	defer rows.Close()
	var out []*domain.Device
	for rows.Next() {
		var d domain.Device
		if err := rows.Scan(&d.ID, &d.PushProvider, &d.PushProviderName, &d.UserID, &d.Disabled, &d.DisabledReason, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// DeleteDevice removes a device registration.
func DeleteDevice(ctx context.Context, q Querier, appID, userID, deviceID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM devices WHERE app_id=$1 AND user_id=$2 AND id=$3`, appID, userID, deviceID)
	if err != nil {
		return fmt.Errorf("store: delete device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
