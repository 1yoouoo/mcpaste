alter table workspaces
    add column event_retention_floor bigint not null default 0
        check (event_retention_floor >= 0);
