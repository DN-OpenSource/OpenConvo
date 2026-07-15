package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/openstream/openstream/internal/domain"
)

const appColumns = `id, name, api_key, api_secret, settings, created_at, updated_at`

func scanApp(row interface{ Scan(...any) error }) (*domain.App, error) {
	var a domain.App
	var settings []byte
	if err := row.Scan(&a.ID, &a.Name, &a.APIKey, &a.APISecret, &settings, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(settings, &a.Settings); err != nil {
		return nil, fmt.Errorf("store: app settings: %w", err)
	}
	return &a, nil
}

// CreateApp provisions a new app with generated credentials and seeds the
// built-in channel types and roles (SPEC.md §6.2, §7.1).
func (s *Store) CreateApp(ctx context.Context, name string) (*domain.App, error) {
	apiKey, err := randomToken(12)
	if err != nil {
		return nil, err
	}
	apiSecret, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	var app *domain.App
	err = s.InTx(ctx, func(tx Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO apps (name, api_key, api_secret)
			VALUES ($1, $2, $3)
			RETURNING `+appColumns, name, apiKey, apiSecret)
		app, err = scanApp(row)
		if err != nil {
			return fmt.Errorf("store: create app: %w", err)
		}
		if err := EnsureBuiltinChannelTypes(ctx, tx, app.ID); err != nil {
			return err
		}
		return EnsureBuiltinRoles(ctx, tx, app.ID)
	})
	if err != nil {
		return nil, err
	}
	return app, nil
}

// GetAppByKey resolves an app by its public api_key (auth middleware).
func GetAppByKey(ctx context.Context, q Querier, apiKey string) (*domain.App, error) {
	row := q.QueryRow(ctx, `SELECT `+appColumns+` FROM apps WHERE api_key = $1`, apiKey)
	app, err := scanApp(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get app by key")
	}
	return app, nil
}

// GetApp fetches an app by id.
func GetApp(ctx context.Context, q Querier, id string) (*domain.App, error) {
	row := q.QueryRow(ctx, `SELECT `+appColumns+` FROM apps WHERE id = $1`, id)
	app, err := scanApp(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get app")
	}
	return app, nil
}

// ListApps returns every app on the cluster (CLI/admin use).
func ListApps(ctx context.Context, q Querier) ([]*domain.App, error) {
	rows, err := q.Query(ctx, `SELECT `+appColumns+` FROM apps ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list apps: %w", err)
	}
	defer rows.Close()
	var apps []*domain.App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// UpdateAppSettings replaces the settings document.
func UpdateAppSettings(ctx context.Context, q Querier, appID string, settings domain.AppSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("store: marshal settings: %w", err)
	}
	tag, err := q.Exec(ctx, `UPDATE apps SET settings = $2, updated_at = now() WHERE id = $1`, appID, raw)
	if err != nil {
		return fmt.Errorf("store: update app settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: token entropy: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
