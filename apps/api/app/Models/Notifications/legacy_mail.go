package notifications

import (
	"context"
	"fmt"
	"strings"
)

const (
	legacyMailMigrationKey = "mail_provider_plugin_v1"
	builtinSMTPPluginID    = "sforum.smtp"
)

var legacyToPluginSetting = map[string]string{
	"mail.from_address": "from_address", "mail.from_name": "from_name", "mail.smtp.host": "host",
	"mail.smtp.port": "port", "mail.smtp.username": "username", "mail.smtp.password": "password",
	"mail.smtp.encryption": "encryption",
}

// legacyMailSite distinguishes a pre-plugin installation from a fresh
// database. A fresh install has no legacy mail keys; it must leave the
// selection row absent so the registry can resolve the recommended default.
func legacyMailSite(values map[string]string) bool {
	if strings.TrimSpace(values["mail.provider"]) != "" {
		return true
	}
	for key := range legacyToPluginSetting {
		if strings.TrimSpace(values[key]) != "" {
			return true
		}
	}
	return false
}

func legacyMailSettings(legacy, current map[string]string) (map[string]string, bool) {
	settings := make(map[string]string, len(current)+len(legacyToPluginSetting))
	for key, value := range current {
		settings[key] = value
	}
	for oldKey, newKey := range legacyToPluginSetting {
		if _, exists := settings[newKey]; !exists {
			settings[newKey] = legacy[oldKey]
		}
	}
	return settings, legacy["mail.provider"] == "smtp"
}

func (s *PostgresStore) AdoptLegacyMail(ctx context.Context, legacy map[string]string) error {
	if s.pool == nil {
		return fmt.Errorf("notifications: postgres pool is required for legacy mail adoption")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, legacyMailMigrationKey); err != nil {
		return err
	}
	var completed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_migrations WHERE key=$1)`, legacyMailMigrationKey).Scan(&completed); err != nil {
		return err
	}
	if completed {
		return tx.Commit(ctx)
	}
	var extensionExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extensions WHERE id=$1 AND type='plugin')`, builtinSMTPPluginID).Scan(&extensionExists); err != nil {
		return err
	}
	if !extensionExists {
		return fmt.Errorf("notifications: builtin smtp plugin is not synchronized")
	}
	rows, err := tx.Query(ctx, `SELECT name, value FROM extension_settings WHERE extension_id=$1`, builtinSMTPPluginID)
	if err != nil {
		return err
	}
	current := map[string]string{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		current[key] = value
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	settings, selectSMTP := legacyMailSettings(legacy, current)
	for key, value := range settings {
		if _, exists := current[key]; exists {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO extension_settings (extension_id, name, value) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, builtinSMTPPluginID, key, value); err != nil {
			return err
		}
	}
	if selectSMTP {
		if _, err = tx.Exec(ctx, `UPDATE extensions SET status='enabled', updated_at=NOW() WHERE id=$1`, builtinSMTPPluginID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO mail_provider_selection (slot, extension_id) VALUES ('mail.provider',$1) ON CONFLICT (slot) DO UPDATE SET extension_id=EXCLUDED.extension_id, updated_at=NOW()`, builtinSMTPPluginID); err != nil {
			return err
		}
	} else if legacyMailSite(legacy) {
		if _, err = tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE slot='mail.provider'`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runtime_migrations (key) VALUES ($1)`, legacyMailMigrationKey); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
