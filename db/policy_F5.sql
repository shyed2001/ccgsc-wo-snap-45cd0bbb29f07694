-- Path: db/policy_F5.sql | Codex | 2026-11-18
-- T42 F5: optional three-layer policy section for the existing pool page schemas.
-- Apply manually AFTER pool_config.sql. No runtime migration. Preserve schema history.
-- Idempotent for the same schema version; fails on a conflicting existing F5 version.
BEGIN;
DO $F5$
DECLARE
    p text;
    prefix text;
    old pool_config_json_schema%ROWTYPE;
    next_version text;
    extended jsonb;
BEGIN
    FOREACH p IN ARRAY ARRAY['thread-pool-admin','process-pool-admin'] LOOP
        PERFORM pool_config_lock_page(p);
        SELECT * INTO STRICT old FROM pool_config_json_schema WHERE page=p AND is_current FOR UPDATE;
        IF old.schema->'properties' ? (CASE WHEN p='thread-pool-admin' THEN 'tp.policy' ELSE 'pp.policy' END) THEN
            CONTINUE;
        END IF;
        prefix := CASE WHEN p='thread-pool-admin' THEN 'tp.policy' ELSE 'pp.policy' END;
        next_version := old.schema_version || '-F5';
        extended := jsonb_set(old.schema, ARRAY['properties',prefix], $policy${"type":"object","additionalProperties":false,"required":["adminLimits","poolDefault"],"properties":{"adminLimits":{"type":"object","additionalProperties":false,"properties":{"policy":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"needs":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"priority":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"maxAttempts":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"batchSize":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"threshold":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}},"rerunPercent":{"type":"object","additionalProperties":false,"required":["allowOverride"],"properties":{"allowOverride":{"type":"boolean"},"min":{"type":"number"},"max":{"type":"number"},"allowed":{"type":"array","items":{"type":"string"},"minItems":1}}}}},"poolDefault":{"type":"object","additionalProperties":false,"properties":{"policy":{"type":"string"},"needs":{"type":"string"},"priority":{"type":"string"},"maxAttempts":{"type":"number"},"batchSize":{"type":"number"},"threshold":{"type":"number"},"rerunPercent":{"type":"number"}}}}}$policy$::jsonb, true);
        UPDATE pool_config_json_schema SET is_current=false WHERE page=p AND is_current;
        INSERT INTO pool_config_json_schema(page,schema_name,schema_version,schema,is_current)
        VALUES(p,old.schema_name,next_version,extended,true);
    END LOOP;
END;
$F5$;
COMMIT;
