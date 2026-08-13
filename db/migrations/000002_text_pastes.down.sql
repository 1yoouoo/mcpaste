drop table paste_revisions;
drop table pastes;

alter table workspace_events
    drop constraint workspace_events_event_type_check;

alter table workspace_events
    add constraint workspace_events_event_type_check check (event_type in (
        'device.added', 'device.renamed', 'device.revoked', 'recovery.rotated'
    ));
