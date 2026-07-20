-- Minimal LiteLLM schema slice for auth integration tests: exactly the
-- tables and columns touched by QueryValidateTokenWithHierarchy,
-- QueryLookupDeprecatedToken and QuerySelectAccessGroups.

CREATE TABLE "LiteLLM_VerificationToken" (
    token text PRIMARY KEY,
    key_name text,
    key_alias text,
    user_id text,
    team_id text,
    organization_id text,
    spend double precision NOT NULL DEFAULT 0,
    max_budget double precision,
    tpm_limit bigint,
    rpm_limit bigint,
    expires timestamp(3),
    blocked boolean,
    models text[] NOT NULL DEFAULT '{}',
    allowed_routes text[] NOT NULL DEFAULT '{}',
    project_id text,
    agent_id text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    access_group_ids text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE "LiteLLM_UserTable" (
    user_id text PRIMARY KEY,
    user_alias text,
    user_email text,
    max_budget double precision,
    spend double precision NOT NULL DEFAULT 0,
    tpm_limit bigint,
    rpm_limit bigint,
    models text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE "LiteLLM_TeamTable" (
    team_id text PRIMARY KEY,
    team_alias text,
    organization_id text,
    max_budget double precision,
    spend double precision NOT NULL DEFAULT 0,
    blocked boolean NOT NULL DEFAULT false,
    tpm_limit bigint,
    rpm_limit bigint,
    models text[] NOT NULL DEFAULT '{}',
    access_group_ids text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE "LiteLLM_ProjectTable" (
    project_id text PRIMARY KEY,
    models text[] NOT NULL DEFAULT '{}',
    blocked boolean NOT NULL DEFAULT false
);

CREATE TABLE "LiteLLM_OrganizationTable" (
    organization_id text PRIMARY KEY,
    spend double precision NOT NULL DEFAULT 0,
    budget_id text
);

CREATE TABLE "LiteLLM_BudgetTable" (
    budget_id text PRIMARY KEY,
    max_budget double precision,
    tpm_limit bigint,
    rpm_limit bigint,
    allowed_models text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE "LiteLLM_TeamMembership" (
    team_id text NOT NULL,
    user_id text NOT NULL,
    spend double precision NOT NULL DEFAULT 0,
    budget_id text,
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE "LiteLLM_OrganizationMembership" (
    organization_id text NOT NULL,
    user_id text NOT NULL,
    spend double precision NOT NULL DEFAULT 0,
    budget_id text,
    PRIMARY KEY (organization_id, user_id)
);

CREATE TABLE "LiteLLM_DeprecatedVerificationToken" (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    token text NOT NULL UNIQUE,
    active_token_id text NOT NULL,
    revoke_at timestamp(3) NOT NULL,
    created_at timestamp(3) NOT NULL DEFAULT now()
);

CREATE TABLE "LiteLLM_AccessGroupTable" (
    access_group_id text PRIMARY KEY,
    access_group_name text,
    access_model_names text[] NOT NULL DEFAULT '{}',
    assigned_team_ids text[] NOT NULL DEFAULT '{}',
    assigned_key_ids text[] NOT NULL DEFAULT '{}'
);
