package store

import (
	"context"
	"fmt"
)

const usageStatsMirrorTrigger = "usage_logs_mirror_usage_stats_v1"

// BackfillUsageStats copies every successful legacy diagnostic row into the
// append-only statistics facts. source_log_id makes the operation idempotent,
// so startup and old-backup recovery can safely run it more than once.
func BackfillUsageStats(ctx context.Context, ex RowExecer) (int64, error) {
	result, err := ex.ExecContext(ctx, `INSERT INTO usage_stats(
		source_log_id, user_id, conversation_id, message_id, model_id, purpose,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		images_count, cost, currency, credits, workspace_id, channel_id,
		fallback, ttft_fallback_model, created_at
	)
	SELECT id, user_id, conversation_id, message_id, model_id, purpose,
	       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
	       images_count, cost, currency, credits, workspace_id, channel_id,
	       fallback, ttft_fallback_model, created_at
	  FROM usage_logs
	 WHERE COALESCE(status,'ok') <> 'error'
	ON CONFLICT(source_log_id) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("backfill usage stats: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count backfilled usage stats: %w", err)
	}
	return inserted, nil
}

// EnableUsageStatsMirror installs the database-level mirror from successful
// usage_logs inserts to usage_stats. Keeping this in the database also covers
// older application instances during a rolling deployment.
func EnableUsageStatsMirror(ctx context.Context, ex RowExecer) error {
	if usePostgres {
		if _, err := ex.ExecContext(ctx, `CREATE OR REPLACE FUNCTION mirror_usage_log_to_stats_v1()
			RETURNS trigger AS $usage_stats$
			BEGIN
			  IF COALESCE(NEW.status, 'ok') <> 'error' THEN
			    INSERT INTO usage_stats(
			      source_log_id, user_id, conversation_id, message_id, model_id, purpose,
			      input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			      images_count, cost, currency, credits, workspace_id, channel_id,
			      fallback, ttft_fallback_model, created_at
			    ) VALUES (
			      NEW.id, NEW.user_id, NEW.conversation_id, NEW.message_id, NEW.model_id, NEW.purpose,
			      NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
			      NEW.images_count, NEW.cost, NEW.currency, NEW.credits, NEW.workspace_id, NEW.channel_id,
			      NEW.fallback, NEW.ttft_fallback_model, NEW.created_at
			    ) ON CONFLICT(source_log_id) DO NOTHING;
			  END IF;
			  RETURN NEW;
			END;
			$usage_stats$ LANGUAGE plpgsql`); err != nil {
			return fmt.Errorf("create PostgreSQL usage stats mirror function: %w", err)
		}
		if _, err := ex.ExecContext(ctx, `DO $usage_stats_trigger$
			BEGIN
			  CREATE TRIGGER usage_logs_mirror_usage_stats_v1
			  AFTER INSERT ON usage_logs
			  FOR EACH ROW EXECUTE FUNCTION mirror_usage_log_to_stats_v1();
			EXCEPTION WHEN duplicate_object THEN
			  NULL;
			END;
			$usage_stats_trigger$`); err != nil {
			return fmt.Errorf("create PostgreSQL usage stats mirror trigger: %w", err)
		}
		return nil
	}

	_, err := ex.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS usage_logs_mirror_usage_stats_v1
		AFTER INSERT ON usage_logs
		WHEN COALESCE(NEW.status, 'ok') <> 'error'
		BEGIN
		  INSERT INTO usage_stats(
		    source_log_id, user_id, conversation_id, message_id, model_id, purpose,
		    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		    images_count, cost, currency, credits, workspace_id, channel_id,
		    fallback, ttft_fallback_model, created_at
		  ) VALUES (
		    NEW.id, NEW.user_id, NEW.conversation_id, NEW.message_id, NEW.model_id, NEW.purpose,
		    NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
		    NEW.images_count, NEW.cost, NEW.currency, NEW.credits, NEW.workspace_id, NEW.channel_id,
		    NEW.fallback, NEW.ttft_fallback_model, NEW.created_at
		  ) ON CONFLICT(source_log_id) DO NOTHING;
		END`)
	if err != nil {
		return fmt.Errorf("create SQLite usage stats mirror trigger: %w", err)
	}
	return nil
}

// DisableUsageStatsMirror is used only while a full logical backup is being
// restored. The archive's usage_stats file is authoritative; older archives
// without it are explicitly backfilled before the trigger is re-enabled.
func DisableUsageStatsMirror(ctx context.Context, ex RowExecer) error {
	if usePostgres {
		if _, err := ex.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+usageStatsMirrorTrigger+` ON usage_logs`); err != nil {
			return fmt.Errorf("disable PostgreSQL usage stats mirror: %w", err)
		}
		return nil
	}
	if _, err := ex.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+usageStatsMirrorTrigger); err != nil {
		return fmt.Errorf("disable SQLite usage stats mirror: %w", err)
	}
	return nil
}
