drop table paste_assets;
alter table paste_revisions drop constraint paste_revisions_workspace_id_unique;
alter table paste_revisions drop constraint paste_revisions_body_fields;
alter table paste_revisions drop constraint paste_revisions_exact_retention;
alter table paste_revisions add constraint paste_revisions_exact_retention check (expires_at = created_at + interval '1 year');
alter table paste_revisions add constraint paste_revisions_body_fields check (
    (revision_kind = 'tombstone' and text_key_id is null and text_nonce is null and text_ciphertext is null)
    or
    (revision_kind = 'content' and text_key_id is not null and text_nonce is not null
        and octet_length(text_nonce) = 12 and text_ciphertext is not null)
);
alter table paste_revisions drop constraint paste_revisions_revision_kind_check;
alter table paste_revisions add constraint paste_revisions_revision_kind_check check (revision_kind in ('content', 'tombstone'));
alter table pastes drop constraint pastes_paste_kind_check;
alter table pastes drop column paste_kind;
