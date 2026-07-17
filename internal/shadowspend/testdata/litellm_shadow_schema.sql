-- LiteLLM shadow-write integration schema extracted from schema.prisma at
-- commit 10d5804b3ef4ead5abb77c3f5fafdd0c0159de0f. It contains only the tables
-- touched by AIR's shadow writer and is installed into an ephemeral test schema.

CREATE TABLE "LiteLLM_SpendLogs" (
    request_id text PRIMARY KEY,
    call_type text NOT NULL,
    api_key text NOT NULL DEFAULT '',
    spend double precision NOT NULL DEFAULT 0,
    total_tokens integer NOT NULL DEFAULT 0,
    prompt_tokens integer NOT NULL DEFAULT 0,
    completion_tokens integer NOT NULL DEFAULT 0,
    "startTime" timestamptz NOT NULL,
    "endTime" timestamptz NOT NULL,
    request_duration_ms integer,
    "completionStartTime" timestamptz,
    model text NOT NULL DEFAULT '',
    model_id text DEFAULT '',
    model_group text DEFAULT '',
    custom_llm_provider text DEFAULT '',
    api_base text DEFAULT '',
    "user" text DEFAULT '',
    metadata jsonb DEFAULT '{}'::jsonb,
    cache_hit text DEFAULT '',
    cache_key text DEFAULT '',
    request_tags jsonb DEFAULT '[]'::jsonb,
    team_id text,
    organization_id text,
    end_user text,
    requester_ip_address text,
    messages jsonb DEFAULT '{}'::jsonb,
    response jsonb DEFAULT '{}'::jsonb,
    session_id text,
    status text,
    mcp_namespaced_tool_name text,
    agent_id text,
    proxy_server_request jsonb DEFAULT '{}'::jsonb
);
CREATE INDEX spend_logs_start_time_idx ON "LiteLLM_SpendLogs" ("startTime");
CREATE INDEX spend_logs_start_request_idx ON "LiteLLM_SpendLogs" ("startTime", request_id);

CREATE TABLE "LiteLLM_VerificationToken" (
    token text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    model_spend jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_active timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_UserTable" (
    user_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    model_spend jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_TeamTable" (
    team_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    model_spend jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_OrganizationTable" (
    organization_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    model_spend jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_ProjectTable" (
    project_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    model_spend jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_TeamMembership" (
    team_id text NOT NULL,
    user_id text NOT NULL,
    spend double precision NOT NULL DEFAULT 0,
    total_spend double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (team_id, user_id)
);
CREATE TABLE "LiteLLM_OrganizationMembership" (
    organization_id text NOT NULL,
    user_id text NOT NULL,
    spend double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE TABLE "LiteLLM_EndUserTable" (
    user_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0
);
CREATE TABLE "LiteLLM_TagTable" (
    tag_name text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_AgentsTable" (
    agent_id text PRIMARY KEY,
    agent_name text UNIQUE,
    spend double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE "LiteLLM_ToolTable" (
    tool_id text PRIMARY KEY,
    tool_name text NOT NULL UNIQUE,
    origin text NOT NULL,
    input_policy text NOT NULL,
    output_policy text NOT NULL,
    call_count bigint NOT NULL DEFAULT 0,
    key_hash text,
    team_id text,
    key_alias text,
    user_agent text,
    last_used_at timestamptz,
    created_by text,
    updated_by text,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE "LiteLLM_DailyUserSpend" (
    id uuid PRIMARY KEY,
    user_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);

CREATE TABLE "LiteLLM_DailyTeamSpend" (
    id uuid PRIMARY KEY,
    team_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);

CREATE TABLE "LiteLLM_DailyOrganizationSpend" (
    id uuid PRIMARY KEY,
    organization_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);

CREATE TABLE "LiteLLM_DailyEndUserSpend" (
    id uuid PRIMARY KEY,
    end_user_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (end_user_id, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);

CREATE TABLE "LiteLLM_DailyAgentSpend" (
    id uuid PRIMARY KEY,
    agent_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_id, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);

CREATE TABLE "LiteLLM_DailyTagSpend" (
    id uuid PRIMARY KEY,
    tag text,
    request_id text,
    date text NOT NULL,
    api_key text NOT NULL,
    model text,
    model_group text,
    custom_llm_provider text,
    mcp_namespaced_tool_name text,
    endpoint text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    api_requests bigint NOT NULL DEFAULT 0,
    successful_requests bigint NOT NULL DEFAULT 0,
    failed_requests bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tag, date, api_key, model, custom_llm_provider, mcp_namespaced_tool_name, endpoint)
);
