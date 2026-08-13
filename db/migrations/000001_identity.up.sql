create table workspaces (
    id uuid primary key default gen_random_uuid(),
    next_event_sequence bigint not null default 0 check (next_event_sequence >= 0),
    created_at timestamptz not null default transaction_timestamp()
);

create table devices (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null references workspaces(id) on delete cascade,
    display_name varchar(80) not null,
    platform text not null check (platform in ('macos', 'linux')),
    role text not null check (role in ('full', 'connector')),
    created_at timestamptz not null default transaction_timestamp(),
    revoked_at timestamptz,
    constraint devices_full_is_macos check (role <> 'full' or platform = 'macos'),
    constraint devices_display_name_trimmed check (display_name = btrim(display_name)),
    constraint devices_display_name_nonempty check (char_length(display_name) between 1 and 80),
    constraint devices_workspace_id_id_unique unique (workspace_id, id)
);

create unique index devices_workspace_display_name_ci_unique
    on devices (workspace_id, lower(display_name));
create index devices_workspace_created_index
    on devices (workspace_id, created_at, id);
create index devices_workspace_revoked_index
    on devices (workspace_id, revoked_at);

create table credentials (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null,
    device_id uuid not null,
    token_id varchar(22) not null,
    scope text not null check (scope in ('full', 'connector')),
    secret_hash bytea not null check (octet_length(secret_hash) = 32),
    created_at timestamptz not null default transaction_timestamp(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    constraint credentials_workspace_device_fk
        foreign key (workspace_id, device_id)
        references devices(workspace_id, id) on delete cascade,
    constraint credentials_workspace_token_unique unique (workspace_id, token_id),
    constraint credentials_device_scope_unique unique (device_id, scope),
    constraint credentials_token_id_shape check (token_id ~ '^[A-Za-z0-9_-]{22}$')
);

create index credentials_workspace_device_index
    on credentials (workspace_id, device_id);
create index credentials_active_lookup_index
    on credentials (workspace_id, token_id)
    where revoked_at is null;

create table recovery_verifiers (
    workspace_id uuid primary key references workspaces(id) on delete cascade,
    locator varchar(22) not null,
    salt bytea not null check (octet_length(salt) = 16),
    verifier bytea not null check (octet_length(verifier) = 32),
    argon_version smallint not null check (argon_version = 19),
    argon_time integer not null check (argon_time = 3),
    argon_memory_kib integer not null check (argon_memory_kib = 65536),
    argon_threads smallint not null check (argon_threads = 4),
    created_at timestamptz not null default transaction_timestamp(),
    rotated_at timestamptz not null default transaction_timestamp(),
    constraint recovery_workspace_locator_unique unique (workspace_id, locator),
    constraint recovery_locator_shape check (locator ~ '^[A-Za-z0-9_-]{22}$')
);

create table pairing_requests (
    id uuid primary key default gen_random_uuid(),
    short_code char(8) not null unique,
    claim_hash bytea not null check (octet_length(claim_hash) = 32),
    proposed_name varchar(80) not null,
    platform text not null check (platform in ('macos', 'linux')),
    requested_scope text not null check (requested_scope in ('full', 'connector')),
    workspace_id uuid references workspaces(id) on delete cascade,
    approved_by_device_id uuid,
    device_id uuid,
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    approved_at timestamptz,
    claim_expires_at timestamptz,
    claimed_at timestamptz,
    claim_invalidated_at timestamptz,
    grant_key_id varchar(32),
    grant_nonce bytea,
    grant_ciphertext bytea,
    metadata_purge_at timestamptz not null,
    constraint pairing_short_code_shape check (
        short_code ~ '^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{8}$'
    ),
    constraint pairing_full_is_macos check (requested_scope <> 'full' or platform = 'macos'),
    constraint pairing_name_trimmed check (proposed_name = btrim(proposed_name)),
    constraint pairing_name_nonempty check (char_length(proposed_name) between 1 and 80),
    constraint pairing_expiry_after_create check (expires_at > created_at),
    constraint pairing_workspace_approver_fk
        foreign key (workspace_id, approved_by_device_id)
        references devices(workspace_id, id),
    constraint pairing_workspace_device_fk
        foreign key (workspace_id, device_id)
        references devices(workspace_id, id),
    constraint pairing_approval_fields_together check (
        (workspace_id is null and approved_by_device_id is null and device_id is null and
         approved_at is null and claim_expires_at is null and grant_key_id is null and
         grant_nonce is null and grant_ciphertext is null)
        or
        (workspace_id is not null and approved_by_device_id is not null and device_id is not null and
         approved_at is not null and claim_expires_at is not null and grant_key_id is not null and
         grant_nonce is not null and grant_ciphertext is not null)
    ),
    constraint pairing_grant_key_id_shape check (
        grant_key_id is null or grant_key_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$'
    ),
    constraint pairing_grant_nonce_size check (
        grant_nonce is null or octet_length(grant_nonce) = 12
    ),
    constraint pairing_claim_after_approval check (
        claimed_at is null or (approved_at is not null and claimed_at >= approved_at)
    ),
    constraint pairing_claim_terminal_exclusive check (
        claimed_at is null or claim_invalidated_at is null
    ),
    constraint pairing_invalidation_after_approval check (
        claim_invalidated_at is null or
        (approved_at is not null and claim_invalidated_at >= approved_at)
    )
);

create index pairing_pending_expiry_index
    on pairing_requests (expires_at)
    where approved_at is null;
create index pairing_claim_expiry_index
    on pairing_requests (claim_expires_at)
    where approved_at is not null and claimed_at is null and claim_invalidated_at is null;
create index pairing_metadata_purge_index
    on pairing_requests (metadata_purge_at);

create table workspace_events (
    workspace_id uuid not null references workspaces(id) on delete cascade,
    sequence bigint not null check (sequence > 0),
    event_type text not null check (event_type in (
        'device.added', 'device.renamed', 'device.revoked', 'recovery.rotated'
    )),
    object_id uuid not null,
    metadata jsonb not null default '{}'::jsonb check (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    primary key (workspace_id, sequence)
);

create index workspace_events_created_index
    on workspace_events (workspace_id, created_at);
create index workspace_events_expiry_index
    on workspace_events (expires_at);

create table idempotency_records (
    scope_id varchar(36) not null,
    operation varchar(64) not null,
    key_hash bytea not null check (octet_length(key_hash) = 32),
    workspace_id uuid references workspaces(id) on delete cascade,
    request_hash bytea not null check (octet_length(request_hash) = 32),
    response_status smallint not null check (response_status between 200 and 299),
    response_content_type text not null check (response_content_type = 'application/json'),
    response_key_id varchar(32) not null check (
        response_key_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$'
    ),
    response_nonce bytea not null check (octet_length(response_nonce) = 12),
    response_ciphertext bytea not null,
    created_at timestamptz not null,
    expires_at timestamptz not null,
    primary key (scope_id, operation, key_hash),
    constraint idempotency_scope_shape check (
        scope_id = 'public' or
        scope_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ),
    constraint idempotency_exact_lifetime check (expires_at = created_at + interval '24 hours')
);

create index idempotency_scope_workspace_expiry_index
    on idempotency_records (scope_id, workspace_id, expires_at);
create index idempotency_expiry_index
    on idempotency_records (expires_at);

create table rate_limit_buckets (
    scope varchar(64) not null,
    subject_hash bytea not null check (octet_length(subject_hash) = 32),
    window_started_at timestamptz not null,
    request_count integer not null check (request_count > 0),
    expires_at timestamptz not null,
    primary key (scope, subject_hash),
    constraint rate_limit_expiry_after_window check (expires_at > window_started_at)
);

create index rate_limit_expiry_index
    on rate_limit_buckets (expires_at);
