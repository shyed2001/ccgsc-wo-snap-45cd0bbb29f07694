-- Path: db/redis_admin_T91.sql | Author: Codex | 2026-11-20
-- T91 / R42.8: additive schema extension for BOTH existing Director pages.
-- Apply manually after pool_config.sql (and policy_F5.sql when used).
-- No runtime migration; keep prior schema versions and every unrelated property.
BEGIN;
DO $T91$
DECLARE
    p text;
    old pool_config_json_schema%ROWTYPE;
    extended jsonb;
    additions jsonb := $redis${
  "redis.memory.mode": {
    "type": "string",
    "enum": [
      "fixed",
      "dynamic"
    ],
    "default": "fixed",
    "description": "T91 / R42.8: shared Redis cap mode; always noeviction."
  },
  "redis.memory.fixed_gb": {
    "type": "integer",
    "minimum": 1,
    "maximum": 4,
    "default": 1,
    "description": "Fixed cap in binary GB (GiB)."
  },
  "redis.memory.dynamic_percent": {
    "type": "integer",
    "minimum": 10,
    "maximum": 40,
    "default": 10,
    "description": "Percent of Linux MemAvailable, clamped to 1-4 GiB."
  },
  "redis.memory.interval_min": {
    "type": "integer",
    "minimum": 1,
    "maximum": 60,
    "default": 5,
    "description": "Dynamic recomputation interval in minutes."
  },
  "redis.persistence.mode": {
    "type": "string",
    "enum": [
      "aof_everysec",
      "snapshot"
    ],
    "default": "aof_everysec",
    "description": "AOF every second or existing Redis snapshot save rules only."
  }
}$redis$::jsonb;
BEGIN
    FOREACH p IN ARRAY ARRAY['thread-pool-admin','process-pool-admin'] LOOP
        PERFORM pool_config_lock_page(p);
        SELECT * INTO STRICT old FROM pool_config_json_schema WHERE page=p AND is_current FOR UPDATE;
        IF old.schema->'properties' @> additions
           AND old.schema->'propertyNames'->>'pattern' LIKE '%redis%' THEN
            CONTINUE;
        END IF;
        extended := jsonb_set(old.schema, '{properties}', (old.schema->'properties') || additions);
        -- OR with the old expression: preserve every formerly allowed name.
        extended := jsonb_set(extended, '{propertyNames,pattern}', to_jsonb(
            '(' || (old.schema->'propertyNames'->>'pattern') || ')|(^redis\.[a-z0-9_]+(\.[a-z0-9_]+)*$)'));
        UPDATE pool_config_json_schema SET is_current=false WHERE page=p AND is_current;
        INSERT INTO pool_config_json_schema(page,schema_name,schema_version,schema,is_current)
        VALUES(p,old.schema_name,old.schema_version || '-T91',extended,true);
    END LOOP;
END;
$T91$;
COMMIT;
