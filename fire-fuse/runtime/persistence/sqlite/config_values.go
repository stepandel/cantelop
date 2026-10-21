package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
)

const (
	configScopeApp         = "app"
	maxConfigEntries       = 128
	maxConfigBytes         = 512 * 1024
	maxConfigAssignmentLen = 512 * 1024
)

type ConfigValues struct {
	db     *sql.DB
	cipher *configcipher.Cipher
}

func NewConfigValues(db *sql.DB, cipher *configcipher.Cipher) *ConfigValues {
	return &ConfigValues{db: db, cipher: cipher}
}

func (r *ConfigValues) SetApp(appID string, value model.ConfigValue) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if err := validateAppID(appID); err != nil {
		return err
	}
	if err := r.requireApp(appID); err != nil {
		return err
	}
	valueText, nonce, ciphertext, err := r.encode(appID, value)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin configuration update: %w", err)
	}
	defer tx.Rollback()
	current, err := r.read(tx, appID)
	if err != nil {
		return err
	}
	indexed := indexConfigValues(current)
	if existing, ok := indexed[value.Name]; ok && existing.Kind != value.Kind {
		return fmt.Errorf("%w: %s", model.ErrConfigKindConflict, value.Name)
	}
	if err := enforceConfigQuota(indexed, value); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO app_config_values
  (app_id, name, kind, value_text, value_nonce, value_ciphertext, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (app_id, name) DO UPDATE SET
  value_text = excluded.value_text,
  value_nonce = excluded.value_nonce,
  value_ciphertext = excluded.value_ciphertext,
  updated_at = excluded.updated_at`, appID, value.Name, value.Kind, valueText, nonce, ciphertext, formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store app configuration value: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit configuration update: %w", err)
	}
	return nil
}

func (r *ConfigValues) ListApp(appID string) ([]model.ConfigValue, error) {
	if err := validateAppID(appID); err != nil {
		return nil, err
	}
	if err := r.requireApp(appID); err != nil {
		return nil, err
	}
	return r.read(r.db, appID)
}

func (r *ConfigValues) DeleteApp(appID, name string) (bool, error) {
	if !model.ValidConfigName(name) {
		return false, model.ErrInvalidConfigValue
	}
	result, err := r.db.Exec("DELETE FROM app_config_values WHERE app_id = ? AND name = ?", appID, name)
	if err != nil {
		return false, fmt.Errorf("delete configuration value: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect configuration delete: %w", err)
	}
	if rows == 0 {
		if err := r.requireApp(appID); err != nil {
			return false, err
		}
	}
	return rows > 0, nil
}

func (r *ConfigValues) ResolveApp(appID string) ([]string, error) {
	if err := validateAppID(appID); err != nil {
		return nil, err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin configuration resolution: %w", err)
	}
	defer tx.Rollback()
	if err := requireActiveApp(tx, appID); err != nil {
		return nil, err
	}
	values, err := r.read(tx, appID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit configuration resolution: %w", err)
	}
	return configAssignments(values)
}

func (r *ConfigValues) ReplaceApp(appID string, revision int64, values []model.ConfigValue) (bool, error) {
	if revision < 1 {
		return false, model.ErrInvalidConfigValue
	}
	indexed := make(map[string]model.ConfigValue, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return false, err
		}
		if _, exists := indexed[value.Name]; exists {
			return false, model.ErrInvalidConfigValue
		}
		if err := enforceConfigQuota(indexed, value); err != nil {
			return false, err
		}
		indexed[value.Name] = value
	}
	tx, err := r.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin configuration projection: %w", err)
	}
	defer tx.Rollback()
	if err := requireActiveApp(tx, appID); err != nil {
		return false, err
	}
	var current int64
	err = tx.QueryRow("SELECT revision FROM app_config_revisions WHERE app_id = ?", appID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read configuration projection revision: %w", err)
	}
	if revision < current {
		return false, model.ErrStaleConfigProjection
	}
	if revision == current {
		return false, nil
	}
	if _, err := tx.Exec("DELETE FROM app_config_values WHERE app_id = ?", appID); err != nil {
		return false, fmt.Errorf("replace configuration projection: %w", err)
	}
	for _, value := range values {
		valueText, nonce, ciphertext, err := r.encode(appID, value)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(`INSERT INTO app_config_values
  (app_id, name, kind, value_text, value_nonce, value_ciphertext, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, appID, value.Name, value.Kind, valueText, nonce, ciphertext, formatTime(value.UpdatedAt))
		if err != nil {
			return false, fmt.Errorf("insert configuration projection: %w", err)
		}
	}
	_, err = tx.Exec(`INSERT INTO app_config_revisions (app_id, revision) VALUES (?, ?)
ON CONFLICT (app_id) DO UPDATE SET revision = excluded.revision`, appID, revision)
	if err != nil {
		return false, fmt.Errorf("store configuration projection revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit configuration projection: %w", err)
	}
	return true, nil
}

func (r *ConfigValues) read(querier rowsQuerier, appID string) ([]model.ConfigValue, error) {
	rows, err := querier.Query(`SELECT name, kind, value_text, value_nonce, value_ciphertext, updated_at
FROM app_config_values WHERE app_id = ? ORDER BY name`, appID)
	if err != nil {
		return nil, fmt.Errorf("list app configuration: %w", err)
	}
	defer rows.Close()
	values := make([]model.ConfigValue, 0)
	for rows.Next() {
		var value model.ConfigValue
		var valueText sql.NullString
		var nonce, ciphertext []byte
		var updatedAt string
		if err := rows.Scan(&value.Name, &value.Kind, &valueText, &nonce, &ciphertext, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan app configuration: %w", err)
		}
		value.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		switch value.Kind {
		case model.ConfigValueVariable:
			if !valueText.Valid {
				return nil, model.ErrInvalidConfigValue
			}
			value.Value = valueText.String
		case model.ConfigValueSecret:
			value.Value, err = r.open(appID, value.Name, nonce, ciphertext)
			if err != nil {
				return nil, err
			}
		default:
			return nil, model.ErrInvalidConfigValue
		}
		if err := value.Validate(); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate app configuration: %w", err)
	}
	return values, nil
}

func (r *ConfigValues) encode(appID string, value model.ConfigValue) (any, []byte, []byte, error) {
	if value.Kind != model.ConfigValueSecret {
		return value.Value, nil, nil, nil
	}
	nonce, ciphertext, err := r.cipher.Seal(configScopeApp, appID, value.Name, value.Value)
	if errors.Is(err, configcipher.ErrUnavailable) {
		return nil, nil, nil, model.ErrConfigEncryptionRequired
	}
	return nil, nonce, ciphertext, err
}

func (r *ConfigValues) open(appID, name string, nonce, ciphertext []byte) (string, error) {
	value, err := r.cipher.Open(configScopeApp, appID, name, nonce, ciphertext)
	if errors.Is(err, configcipher.ErrUnavailable) {
		return "", model.ErrConfigEncryptionRequired
	}
	if errors.Is(err, configcipher.ErrAuthentication) {
		return "", errors.Join(model.ErrConfigDecryption, err)
	}
	return value, err
}

func (r *ConfigValues) requireApp(appID string) error { return requireApp(r.db, appID, false) }

func requireActiveApp(db rowQuerier, appID string) error { return requireApp(db, appID, true) }

func requireApp(db rowQuerier, appID string, active bool) error {
	query := "SELECT 1 FROM apps WHERE id = ?"
	if active {
		query += " AND deleted_at IS NULL"
	}
	var exists int
	if err := db.QueryRow(query, appID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return model.ErrAppNotFound
	} else if err != nil {
		return fmt.Errorf("check app configuration scope: %w", err)
	}
	return nil
}

func validateAppID(appID string) error {
	if model.ValidateResourceID(appID) != nil {
		return model.ErrInvalidApp
	}
	return nil
}

func indexConfigValues(values []model.ConfigValue) map[string]model.ConfigValue {
	indexed := make(map[string]model.ConfigValue, len(values))
	for _, value := range values {
		indexed[value.Name] = value
	}
	return indexed
}

func enforceConfigQuota(values map[string]model.ConfigValue, replacement model.ConfigValue) error {
	count, total := len(values), len(replacement.Name)+len(replacement.Value)
	if _, exists := values[replacement.Name]; !exists {
		count++
	}
	for name, value := range values {
		if name != replacement.Name {
			total += len(name) + len(value.Value)
		}
	}
	if count > maxConfigEntries || total > maxConfigBytes {
		return model.ErrConfigQuotaExceeded
	}
	return nil
}

func configAssignments(values []model.ConfigValue) ([]string, error) {
	assignments := make([]string, 0, len(values))
	total := 0
	for _, value := range values {
		assignment := value.Name + "=" + value.Value
		total += len(assignment) + 1
		if total > maxConfigAssignmentLen {
			return nil, model.ErrConfigQuotaExceeded
		}
		assignments = append(assignments, assignment)
	}
	return assignments, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
