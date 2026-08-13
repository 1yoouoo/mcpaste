alter table workspace_events
    drop constraint workspace_events_event_type_check;

alter table workspace_events
    add constraint workspace_events_event_type_check check (event_type in (
        'device.added', 'device.renamed', 'device.revoked', 'recovery.rotated',
        'paste.created', 'paste.revised', 'paste.deleted'
    ));

create table pastes (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null references workspaces(id) on delete cascade,
    created_at timestamptz not null default transaction_timestamp(),
    last_mcp_access_at timestamptz,
    mcp_access_count bigint not null default 0 check (mcp_access_count >= 0),
    constraint pastes_workspace_id_unique unique (workspace_id, id)
);

create table paste_revisions (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null,
    paste_id uuid not null,
    server_sequence bigint not null check (server_sequence > 0),
    revision_kind text not null check (revision_kind in ('content', 'tombstone')),
    text_key_id varchar(32),
    text_nonce bytea,
    text_ciphertext bytea,
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    constraint paste_revisions_workspace_paste_fk
        foreign key (workspace_id, paste_id)
        references pastes(workspace_id, id) on delete cascade,
    constraint paste_revisions_workspace_sequence_unique
        unique (workspace_id, server_sequence),
    constraint paste_revisions_exact_retention
        check (expires_at = created_at + interval '1 year'),
    constraint paste_revisions_body_fields check (
        (revision_kind = 'tombstone' and text_key_id is null and text_nonce is null and text_ciphertext is null)
        or
        (revision_kind = 'content' and text_key_id is not null and text_nonce is not null
            and octet_length(text_nonce) = 12 and text_ciphertext is not null)
    ),
    constraint paste_revisions_key_id_shape check (
        text_key_id is null or text_key_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$'
    )
);

create index pastes_workspace_created_index
    on pastes (workspace_id, created_at desc, id);
create index paste_revisions_workspace_sequence_index
    on paste_revisions (workspace_id, server_sequence);
create index paste_revisions_paste_sequence_index
    on paste_revisions (workspace_id, paste_id, server_sequence desc);
create index paste_revisions_expiry_index
    on paste_revisions (expires_at);
