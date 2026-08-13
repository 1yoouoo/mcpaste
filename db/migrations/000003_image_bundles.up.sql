alter table pastes add column paste_kind text not null default 'text';
alter table pastes add constraint pastes_paste_kind_check check (paste_kind in ('text', 'image_bundle'));

alter table paste_revisions drop constraint paste_revisions_revision_kind_check;
alter table paste_revisions add constraint paste_revisions_revision_kind_check check (revision_kind in ('content', 'tombstone', 'image_bundle'));
alter table paste_revisions drop constraint paste_revisions_body_fields;
alter table paste_revisions drop constraint paste_revisions_exact_retention;
alter table paste_revisions add constraint paste_revisions_exact_retention check (
    (revision_kind in ('content', 'tombstone') and expires_at = created_at + interval '1 year')
    or (revision_kind = 'image_bundle' and expires_at = created_at + interval '24 hours')
);
alter table paste_revisions add constraint paste_revisions_body_fields check (
    (revision_kind = 'tombstone' and text_key_id is null and text_nonce is null and text_ciphertext is null)
    or
    (revision_kind = 'content' and text_key_id is not null and text_nonce is not null
        and octet_length(text_nonce) = 12 and text_ciphertext is not null)
    or
    (revision_kind = 'image_bundle' and text_key_id is null and text_nonce is null and text_ciphertext is null)
);
alter table paste_revisions add constraint paste_revisions_workspace_id_unique unique (workspace_id, id);

create table paste_assets (
    id uuid primary key default gen_random_uuid(),
    workspace_id uuid not null,
    paste_id uuid not null,
    revision_id uuid not null references paste_revisions(id) on delete cascade,
    asset_index integer not null check (asset_index >= 0 and asset_index < 20),
    mime_type varchar(127) not null,
    width integer not null check (width > 0),
    height integer not null check (height > 0),
    byte_size bigint not null check (byte_size > 0 and byte_size <= 262144000),
    storage_key text not null,
    image_key_id varchar(32) not null,
    image_nonce bytea not null check (octet_length(image_nonce) = 12),
    created_at timestamptz not null default transaction_timestamp(),
    expires_at timestamptz not null,
    constraint paste_assets_workspace_paste_fk
        foreign key (workspace_id, paste_id) references pastes(workspace_id, id) on delete cascade,
    constraint paste_assets_expiry check (expires_at = created_at + interval '24 hours'),
    constraint paste_assets_revision_workspace_fk
        foreign key (workspace_id, revision_id) references paste_revisions(workspace_id, id) on delete cascade,
    constraint paste_assets_revision_index_unique unique (revision_id, asset_index),
    constraint paste_assets_storage_key_unique unique (storage_key)
);

create index paste_assets_expiry_index on paste_assets (expires_at);
create index paste_assets_paste_index on paste_assets (workspace_id, paste_id, revision_id, asset_index);
