package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sensahin/sheaf/internal/config"
)

// Device is a phone or browser paired to act as the human. Each has its own
// token, so losing a phone means revoking one device, not rotating the owner
// token everywhere.
type Device struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// requireOwnerToken allows only the owner token itself, not a paired device.
// Devices can decide approvals and message agents, but a stolen phone cannot
// pair more devices, mint agent tokens or loosen policies.
func requireOwnerToken(c Caller) error {
	if !c.IsHuman() || c.Device != "" {
		return forbiddenf("this needs the owner token, not a paired device")
	}
	return nil
}

// PairDevice creates a device and returns its token, shown once.
func (s *Service) PairDevice(ctx context.Context, c Caller, name string) (Device, string, error) {
	if err := requireOwnerToken(c); err != nil {
		return Device{}, "", err
	}
	if name == "" {
		name = "device"
	}
	if len(name) > 64 {
		return Device{}, "", invalidf("device name is longer than 64 characters")
	}
	d := Device{ID: newID("d-"), Name: name, CreatedAt: s.Now().UTC()}
	token := config.NewToken("shd_")
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO devices (id, name, token_hash, created_at) VALUES (?, ?, ?, ?)`,
			d.ID, d.Name, hashToken(token), d.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "device.paired", d.ID, map[string]string{"name": d.Name})
	})
	return d, token, err
}

// RevokeDevice stops a device's token from working.
func (s *Service) RevokeDevice(ctx context.Context, c Caller, id string) error {
	if err := requireOwnerToken(c); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.now(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no active device %q", ErrNotFound, id)
		}
		return s.emit(tx, c.Name, "device.revoked", id, nil)
	})
}

// ListDevices returns all devices, revoked ones included.
func (s *Service) ListDevices(ctx context.Context, c Caller) ([]Device, error) {
	if !c.IsHuman() {
		return nil, ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_seen, revoked_at FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var created string
		var seen, revoked sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		d.LastSeen = parseNullTime(seen)
		d.RevokedAt = parseNullTime(revoked)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) authenticateDevice(ctx context.Context, hash string) (Caller, bool, error) {
	var id string
	var seen sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, last_seen FROM devices WHERE token_hash = ? AND revoked_at IS NULL`, hash).Scan(&id, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, false, nil
	}
	if err != nil {
		return Caller{}, false, err
	}
	// Record last use, at most once a minute so polling stays cheap.
	if last := parseNullTime(seen); last == nil || s.Now().Sub(*last) > time.Minute {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ?`, s.now(), id); err != nil {
			return Caller{}, false, err
		}
	}
	return Caller{Name: Human, Device: id}, true, nil
}

func parseNullTime(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	return &t
}
