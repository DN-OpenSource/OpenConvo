package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

const userColumns = `id, name, image, role, teams, online, invisible, banned,
	ban_expires, deactivated_at, deleted_at, last_active,
	revoke_tokens_issued_before, custom, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*domain.User, error) {
	var u domain.User
	var custom []byte
	if err := row.Scan(&u.ID, &u.Name, &u.Image, &u.Role, &u.Teams, &u.Online,
		&u.Invisible, &u.Banned, &u.BanExpires, &u.DeactivatedAt, &u.DeletedAt,
		&u.LastActive, &u.RevokeTokensIssuedBefore, &custom, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	if len(custom) > 0 && string(custom) != "{}" {
		if err := json.Unmarshal(custom, &u.Custom); err != nil {
			return nil, fmt.Errorf("store: user custom: %w", err)
		}
	}
	return &u, nil
}

func customJSON(custom map[string]any) ([]byte, error) {
	if custom == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(custom)
}

// UpsertUser creates or updates a user by developer-provided id (SPEC.md
// §5.3 U1); created_at is preserved on update.
func UpsertUser(ctx context.Context, q Querier, appID string, u *domain.User) (*domain.User, error) {
	if u.Role == "" {
		u.Role = domain.RoleUser
	}
	if u.Teams == nil {
		u.Teams = []string{}
	}
	custom, err := customJSON(u.Custom)
	if err != nil {
		return nil, fmt.Errorf("store: user custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO users (app_id, id, name, image, role, teams, invisible, custom)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (app_id, id) DO UPDATE SET
			name = EXCLUDED.name,
			image = EXCLUDED.image,
			role = EXCLUDED.role,
			teams = EXCLUDED.teams,
			invisible = EXCLUDED.invisible,
			custom = EXCLUDED.custom,
			deleted_at = NULL,
			updated_at = now()
		RETURNING `+userColumns,
		appID, u.ID, u.Name, u.Image, u.Role, u.Teams, u.Invisible, custom)
	saved, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("store: upsert user: %w", err)
	}
	return saved, nil
}

// EnsureUser inserts a minimal user row if absent (token auto-create path);
// existing rows are returned untouched.
func EnsureUser(ctx context.Context, q Querier, appID, userID, role string) (*domain.User, error) {
	if role == "" {
		role = domain.RoleUser
	}
	row := q.QueryRow(ctx, `
		INSERT INTO users (app_id, id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (app_id, id) DO UPDATE SET updated_at = users.updated_at
		RETURNING `+userColumns, appID, userID, role)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("store: ensure user: %w", err)
	}
	return u, nil
}

// GetUser fetches one user.
func GetUser(ctx context.Context, q Querier, appID, id string) (*domain.User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE app_id = $1 AND id = $2`, appID, id)
	u, err := scanUser(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get user")
	}
	return u, nil
}

// GetUsers fetches many users by id, returning them keyed by id.
func GetUsers(ctx context.Context, q Querier, appID string, ids []string) (map[string]*domain.User, error) {
	if len(ids) == 0 {
		return map[string]*domain.User{}, nil
	}
	rows, err := q.Query(ctx, `SELECT `+userColumns+` FROM users WHERE app_id = $1 AND id = ANY($2)`, appID, ids)
	if err != nil {
		return nil, fmt.Errorf("store: get users: %w", err)
	}
	defer rows.Close()
	out := make(map[string]*domain.User, len(ids))
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

// QueryUsers runs a compiled filter (store/filters) with sort and paging.
func QueryUsers(ctx context.Context, q Querier, appID, whereSQL string, args []any, orderBy string, limit, offset int) ([]*domain.User, error) {
	sql := fmt.Sprintf(`SELECT %s FROM users
		WHERE app_id = $%d AND deleted_at IS NULL AND (%s)
		ORDER BY %s LIMIT %d OFFSET %d`,
		userColumns, len(args)+1, whereSQL, orderBy, limit, offset)
	rows, err := q.Query(ctx, sql, append(args, appID)...)
	if err != nil {
		return nil, fmt.Errorf("store: query users: %w", err)
	}
	defer rows.Close()
	var users []*domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// PartialUpdateUser applies set/unset semantics (SPEC.md §5.3 U2). Known
// columns update directly; unknown keys go to the custom document.
func PartialUpdateUser(ctx context.Context, q Querier, appID, id string, set map[string]any, unset []string) (*domain.User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE app_id = $1 AND id = $2 FOR UPDATE`, appID, id)
	u, err := scanUser(row)
	if err != nil {
		return nil, notFoundOr(err, "store: partial update user")
	}
	if u.Custom == nil {
		u.Custom = map[string]any{}
	}
	for key, val := range set {
		switch key {
		case "name":
			if s, ok := val.(string); ok {
				u.Name = s
			}
		case "image":
			if s, ok := val.(string); ok {
				u.Image = s
			}
		case "role":
			if s, ok := val.(string); ok {
				u.Role = s
			}
		case "invisible":
			if b, ok := val.(bool); ok {
				u.Invisible = b
			}
		case "teams":
			if list, ok := val.([]any); ok {
				teams := make([]string, 0, len(list))
				for _, item := range list {
					if s, ok := item.(string); ok {
						teams = append(teams, s)
					}
				}
				u.Teams = teams
			}
		default:
			u.Custom[key] = val
		}
	}
	for _, key := range unset {
		delete(u.Custom, key)
	}
	if err := domain.ValidateCustom(&domain.User{}, u.Custom); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	custom, err := customJSON(u.Custom)
	if err != nil {
		return nil, err
	}
	row = q.QueryRow(ctx, `
		UPDATE users SET name=$3, image=$4, role=$5, teams=$6, invisible=$7,
			custom=$8, updated_at=now()
		WHERE app_id=$1 AND id=$2
		RETURNING `+userColumns,
		appID, id, u.Name, u.Image, u.Role, u.Teams, u.Invisible, custom)
	saved, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("store: partial update user: %w", err)
	}
	return saved, nil
}

// SetUserBanned toggles the denormalized global-ban state.
func SetUserBanned(ctx context.Context, q Querier, appID, id string, banned bool, expires *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET banned=$3, ban_expires=$4, updated_at=now() WHERE app_id=$1 AND id=$2`,
		appID, id, banned, expires)
	if err != nil {
		return fmt.Errorf("store: set user banned: %w", err)
	}
	return nil
}

// SetUserOnline updates the denormalized presence snapshot.
func SetUserOnline(ctx context.Context, q Querier, appID, id string, online bool) error {
	_, err := q.Exec(ctx, `UPDATE users SET online=$3, last_active=now(), updated_at=now() WHERE app_id=$1 AND id=$2`,
		appID, id, online)
	if err != nil {
		return fmt.Errorf("store: set user online: %w", err)
	}
	return nil
}

// DeactivateUser marks a user deactivated (SPEC.md §5.3 U8).
func DeactivateUser(ctx context.Context, q Querier, appID, id string) error {
	tag, err := q.Exec(ctx, `UPDATE users SET deactivated_at=now(), updated_at=now() WHERE app_id=$1 AND id=$2 AND deactivated_at IS NULL`,
		appID, id)
	if err != nil {
		return fmt.Errorf("store: deactivate user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReactivateUser reverses deactivation.
func ReactivateUser(ctx context.Context, q Querier, appID, id string) error {
	tag, err := q.Exec(ctx, `UPDATE users SET deactivated_at=NULL, updated_at=now() WHERE app_id=$1 AND id=$2`,
		appID, id)
	if err != nil {
		return fmt.Errorf("store: reactivate user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDeleteUser tombstones a user (SPEC.md §5.3 U9 soft mode).
func SoftDeleteUser(ctx context.Context, q Querier, appID, id string) error {
	tag, err := q.Exec(ctx, `UPDATE users SET deleted_at=now(), updated_at=now() WHERE app_id=$1 AND id=$2 AND deleted_at IS NULL`,
		appID, id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRevokeTokensIssuedBefore sets the per-user token revocation watermark.
func SetRevokeTokensIssuedBefore(ctx context.Context, q Querier, appID, id string, before *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET revoke_tokens_issued_before=$3, updated_at=now() WHERE app_id=$1 AND id=$2`,
		appID, id, before)
	if err != nil {
		return fmt.Errorf("store: set token revocation: %w", err)
	}
	return nil
}
