-- ClickHouse schema for the Kafka -> ClickHouse spend-log analytics pipeline
-- (air.spend_logs). This is the reference DDL from
-- auto_ai_router_kafka_spend_log_tz.md, section 8, verbatim except for
-- `kafka_broker_list`, which is adapted to the `kafka` service name/port used
-- by docker-compose.kafka.yml (the internal PLAINTEXT listener, kafka:29092).
--
-- Mounted into clickhouse-server via docker-entrypoint-initdb.d, so it only
-- runs automatically on the container's first start (empty data directory).
--
-- As noted in the TZ (section 8): AIR itself does not create or administer
-- this schema in production -- this file is a reference example for local
-- dev/testing and for DBAs, not an automated migration run by the router.
-- Retention (TTL) and Kafka topic partitioning are likewise out of scope for
-- AIR and are left to whoever operates the target ClickHouse/Kafka cluster.

CREATE DATABASE IF NOT EXISTS air;

CREATE TABLE air.spend_logs_kafka
(
    request_id String,
    start_time DateTime64(3),
    end_time DateTime64(3),
    completion_start_time Nullable(DateTime64(3)),
    duration_ms UInt32,
    ttft_ms Nullable(UInt32),
    call_type String,
    status LowCardinality(String),
    http_status UInt16,
    error_message Nullable(String),
    error_class LowCardinality(Nullable(String)),
    model String,
    real_model String,
    model_id String,
    model_group String,
    credential_name LowCardinality(String),
    credential_type LowCardinality(String),
    credential_base_url String,
    server_router_id LowCardinality(String),
    server_version String,
    prompt_tokens UInt32,
    completion_tokens UInt32,
    total_tokens UInt32,
    cached_input_tokens UInt32,
    reasoning_tokens UInt32,
    total_cost Float64,
    input_cost Float64,
    output_cost Float64,
    user_id String,
    team_id String,
    organization_id String,
    end_user String,
    requester_ip String,
    session_id String,
    body_captured UInt8               -- всегда 0 пока; поле-заглушка под будущий PR
    -- ... остальные поля из раздела 4 (уже плоские, без вложенности)
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list = 'kafka:29092',
    kafka_topic_list = 'air.spend_logs',
    kafka_group_name = 'clickhouse_air_spend_logs',
    kafka_format = 'JSONEachRow',
    -- Go's json.Marshal writes RFC3339 timestamps (e.g. "2026-07-15T10:00:00.000Z"),
    -- which DateTime64 does not parse by default -- best_effort is required, or
    -- every message ends up in the `_error` stream despite being well-formed.
    date_time_input_format = 'best_effort',
    -- Matches the "air.spend_logs" topic's partition count (2, see
    -- docker-compose.kafka.yml's KAFKA_NUM_PARTITIONS for local dev). In
    -- production this must track whatever the topic is actually provisioned
    -- with -- more consumers than partitions just sit idle.
    kafka_num_consumers = 2,
    kafka_handle_error_mode = 'stream';   -- невалидные сообщения не теряются молча

CREATE TABLE air.spend_logs
(
    LIKE air.spend_logs_kafka
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(start_time)
ORDER BY (start_time, team_id, model)
TTL start_time + INTERVAL 90 DAY;  -- пример; конкретное значение и владение таблицей — на стороне DBA/CH-кластера, не AIR

CREATE MATERIALIZED VIEW air.spend_logs_mv TO air.spend_logs AS
SELECT * FROM air.spend_logs_kafka;
