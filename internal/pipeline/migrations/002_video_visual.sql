DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'noema_ingestion_requests'::regclass AND conname = 'noema_request_visual_state_check'
    ) THEN
        ALTER TABLE noema_ingestion_requests
            DROP CONSTRAINT IF EXISTS noema_ingestion_requests_state_check,
            ADD CONSTRAINT noema_request_visual_state_check CHECK (state IN (
                'PENDING', 'CAPTURING', 'ANALYZING', 'CONSOLIDATING', 'PUBLISHING', 'COMPLETED',
                'RETRY_SCHEDULED', 'WAITING_QUOTA', 'ACCESS_REQUIRED', 'NEEDS_REVIEW', 'FAILED'
            ));
    END IF;
END;
$$;
