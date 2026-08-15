alter table pairing_requests
    drop constraint pairing_invalidation_after_approval;

alter table pairing_requests
    add constraint pairing_invalidation_after_approval check (
        claim_invalidated_at is null or
        (approved_at is not null and claim_invalidated_at >= approved_at) or
        (approved_at is null and claim_invalidated_at >= created_at)
    );
