package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

const (
	attachmentWorkspaceID = "00000000-0000-4000-8000-000000000a01"
	attachmentOtherWSID   = "00000000-0000-4000-8000-000000000a02"
	attachmentPasteID     = "00000000-0000-4000-8000-000000000a03"
	attachmentTextID      = "00000000-0000-4000-8000-000000000a04"
	attachmentOldID       = "00000000-0000-4000-8000-000000000a05"
	attachmentDeviceID    = "00000000-0000-4000-8000-000000000a06"
)

var errAttachmentTest = errors.New("attachment test failure")
var errAttachmentVerification = errors.New("attachment verification failure")
var errAttachmentCleanup = errors.New("attachment cleanup failure")

type attachmentClock struct{ now time.Time }

func (c attachmentClock) Now() time.Time { return c.now }

type attachmentRandom struct {
	next  uint64
	reads int
}

func (r *attachmentRandom) Read(target []byte) (int, error) {
	r.reads++
	for offset := 0; offset < len(target); {
		var seed [8]byte
		binary.LittleEndian.PutUint64(seed[:], r.next)
		digest := sha256.Sum256(seed[:])
		r.next++
		offset += copy(target[offset:], digest[:])
	}
	return len(target), nil
}

type attachmentStore struct {
	Store
	tx            *attachmentTx
	withinTxCalls int
	commitErr     error
	commitErrAt   int
	appliedErr    error
	appliedErrAt  int
	afterApplied  func(*attachmentTx)
	readErr       error
	readErrAt     int
	inTx          bool
	events        []string
}

func (s *attachmentStore) WithinTx(ctx context.Context, fn func(TxStore) error) error {
	s.withinTxCalls++
	s.events = append(s.events, "tx:start")
	if s.readErr != nil && s.withinTxCalls == s.readErrAt {
		s.events = append(s.events, "tx:end")
		return s.readErr
	}
	s.inTx = true
	before := s.tx.snapshot()
	err := fn(s.tx)
	s.inTx = false
	s.events = append(s.events, "tx:end")
	if err != nil {
		s.tx.restore(before)
		return err
	}
	if s.commitErr != nil && (s.commitErrAt == 0 || s.withinTxCalls == s.commitErrAt) {
		err := s.commitErr
		s.commitErr = nil
		s.tx.restore(before)
		return err
	}
	if s.appliedErr != nil && (s.appliedErrAt == 0 || s.withinTxCalls == s.appliedErrAt) {
		err := s.appliedErr
		s.appliedErr = nil
		if s.afterApplied != nil {
			s.afterApplied(s.tx)
		}
		return err
	}
	return nil
}

type attachmentTx struct {
	TxStore
	records    map[string]IdempotencyRecord
	aggregates map[string]PasteAggregate
	nextSeq    int64

	appendAttachmentErr error
	aggregateErr        error
	putIdempotencyErr   error
	currentAssetErr     error
	lockInjection       *IdempotencyRecord

	appendAttachmentCalls int
	appendTextCalls       int
	pasteAggregateCalls   int
	listAggregateCalls    int
	snapshotCalls         int
	currentAssetCalls     int
	latestAggregateCalls  int
	touchPasteCalls       int
	setPasteKindCalls     int
	legacyListCalls       int
	legacySnapshotCalls   int
	latestPasteCalls      int
	lockCalls             int

	lastLockedOperation  string
	lastCurrentWorkspace string
	lastCurrentPaste     string
	lastCurrentIndex     int
	lastListCutoff       time.Time
	lastListNow          time.Time

	listResult     []PasteAggregate
	snapshotResult []PasteAggregate
	snapshotCursor int64
	events         *[]string
}

type attachmentTxState struct {
	records    map[string]IdempotencyRecord
	aggregates map[string]PasteAggregate
	nextSeq    int64
}

func newAttachmentTx() *attachmentTx {
	return &attachmentTx{
		records:    make(map[string]IdempotencyRecord),
		aggregates: make(map[string]PasteAggregate),
	}
}

func (tx *attachmentTx) snapshot() attachmentTxState {
	records := make(map[string]IdempotencyRecord, len(tx.records))
	for key, record := range tx.records {
		records[key] = cloneIdempotencyRecord(record)
	}
	aggregates := make(map[string]PasteAggregate, len(tx.aggregates))
	for key, aggregate := range tx.aggregates {
		aggregates[key] = clonePasteAggregate(aggregate)
	}
	return attachmentTxState{records: records, aggregates: aggregates, nextSeq: tx.nextSeq}
}

func (tx *attachmentTx) restore(state attachmentTxState) {
	tx.records = state.records
	tx.aggregates = state.aggregates
	tx.nextSeq = state.nextSeq
}

func (tx *attachmentTx) LockIdempotency(_ context.Context, scopeID, operation string, keyHash []byte) error {
	tx.lockCalls++
	tx.lastLockedOperation = operation
	if tx.events != nil {
		*tx.events = append(*tx.events, "idempotency:lock")
	}
	if tx.lockInjection != nil {
		record := cloneIdempotencyRecord(*tx.lockInjection)
		record.ScopeID = scopeID
		record.Operation = operation
		record.KeyHash = bytes.Clone(keyHash)
		tx.records[idempotencyRecordKey(scopeID, operation, keyHash)] = record
		tx.lockInjection = nil
	}
	return nil
}

func (tx *attachmentTx) GetIdempotency(_ context.Context, scopeID, operation string, keyHash []byte) (IdempotencyRecord, error) {
	record, ok := tx.records[idempotencyRecordKey(scopeID, operation, keyHash)]
	if !ok {
		return IdempotencyRecord{}, ErrNotFound
	}
	return cloneIdempotencyRecord(record), nil
}

func (tx *attachmentTx) DeleteIdempotency(_ context.Context, scopeID, operation string, keyHash []byte) error {
	delete(tx.records, idempotencyRecordKey(scopeID, operation, keyHash))
	return nil
}

func (tx *attachmentTx) PutIdempotency(_ context.Context, record IdempotencyRecord) error {
	if tx.putIdempotencyErr != nil {
		return tx.putIdempotencyErr
	}
	tx.records[idempotencyRecordKey(record.ScopeID, record.Operation, record.KeyHash)] = cloneIdempotencyRecord(record)
	return nil
}

func (tx *attachmentTx) InsertPaste(_ context.Context, workspaceID, pasteID string, createdAt time.Time) error {
	key := aggregateMapKey(workspaceID, pasteID)
	if _, exists := tx.aggregates[key]; exists {
		return ErrInvalid
	}
	tx.aggregates[key] = PasteAggregate{PasteID: pasteID, CreatedAt: createdAt}
	return nil
}

func (tx *attachmentTx) SetPasteKind(context.Context, string, string, string) error {
	tx.setPasteKindCalls++
	return nil
}

func (tx *attachmentTx) AppendTextRevision(_ context.Context, workspaceID, pasteID, revisionID, kind, _ string, envelope secure.Envelope, createdAt, expiresAt time.Time) (TextRevision, error) {
	tx.appendTextCalls++
	tx.nextSeq++
	revision := TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: kind, ServerSequence: tx.nextSeq, CreatedAt: createdAt,
		ExpiresAt: expiresAt, Envelope: cloneEnvelope(envelope),
	}
	key := aggregateMapKey(workspaceID, pasteID)
	aggregate, exists := tx.aggregates[key]
	if !exists {
		return TextRevision{}, ErrNotFound
	}
	aggregate.PasteID = pasteID
	aggregate.RevisionID = revisionID
	aggregate.ServerSequence = revision.ServerSequence
	aggregate.CreatedAt = createdAt
	if kind == RevisionTombstone {
		aggregate.Deleted = true
		aggregate.TextRevision = nil
		aggregate.AttachmentRevision = nil
		aggregate.AttachmentRevisionID = ""
	} else {
		aggregate.Deleted = false
		aggregate.TextExpiresAt = expiresAt
		aggregate.TextRevision = cloneTextRevision(&revision)
	}
	tx.aggregates[key] = aggregate
	return revision, nil
}

func (tx *attachmentTx) AppendAttachmentRevision(_ context.Context, workspaceID, pasteID, revisionID, _ string, assets []ImageAsset, createdAt, expiresAt time.Time) (TextRevision, error) {
	tx.appendAttachmentCalls++
	if tx.appendAttachmentErr != nil {
		return TextRevision{}, tx.appendAttachmentErr
	}
	key := aggregateMapKey(workspaceID, pasteID)
	aggregate, exists := tx.aggregates[key]
	if !exists || aggregate.Deleted {
		return TextRevision{}, ErrNotFound
	}
	tx.nextSeq++
	copiedAssets := make([]ImageAsset, len(assets))
	for index, asset := range assets {
		copiedAssets[index] = cloneImageAsset(asset)
		copiedAssets[index].WorkspaceID = workspaceID
		copiedAssets[index].PasteID = pasteID
		copiedAssets[index].RevisionID = revisionID
		copiedAssets[index].ExpiresAt = expiresAt
	}
	revision := TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: RevisionAttachmentBundle, ServerSequence: tx.nextSeq,
		CreatedAt: createdAt, ExpiresAt: expiresAt, Assets: copiedAssets,
	}
	aggregate.PasteID = pasteID
	aggregate.RevisionID = revisionID
	aggregate.AttachmentRevisionID = revisionID
	aggregate.ServerSequence = revision.ServerSequence
	aggregate.CreatedAt = createdAt
	aggregate.AttachmentExpiresAt = expiresAt
	aggregate.AttachmentRevision = cloneTextRevision(&revision)
	tx.aggregates[key] = aggregate
	return revision, nil
}

func (tx *attachmentTx) PasteAggregate(_ context.Context, workspaceID, pasteID string, _ time.Time) (PasteAggregate, error) {
	tx.pasteAggregateCalls++
	if tx.aggregateErr != nil {
		return PasteAggregate{}, tx.aggregateErr
	}
	aggregate, ok := tx.aggregates[aggregateMapKey(workspaceID, pasteID)]
	if !ok {
		return PasteAggregate{}, ErrNotFound
	}
	return clonePasteAggregate(aggregate), nil
}

func (tx *attachmentTx) ListPasteAggregates(_ context.Context, _ string, cutoff, now time.Time) ([]PasteAggregate, error) {
	tx.listAggregateCalls++
	tx.lastListCutoff = cutoff
	tx.lastListNow = now
	return clonePasteAggregates(tx.listResult), nil
}

func (tx *attachmentTx) SnapshotAggregates(_ context.Context, _ string, _ time.Time) (int64, []PasteAggregate, error) {
	tx.snapshotCalls++
	return tx.snapshotCursor, clonePasteAggregates(tx.snapshotResult), nil
}

func (tx *attachmentTx) CurrentAttachmentAsset(_ context.Context, workspaceID, pasteID string, assetIndex int, now time.Time) (ImageAsset, error) {
	tx.currentAssetCalls++
	tx.lastCurrentWorkspace = workspaceID
	tx.lastCurrentPaste = pasteID
	tx.lastCurrentIndex = assetIndex
	if tx.currentAssetErr != nil {
		return ImageAsset{}, tx.currentAssetErr
	}
	aggregate, ok := tx.aggregates[aggregateMapKey(workspaceID, pasteID)]
	if !ok || aggregate.Deleted || aggregate.AttachmentRevision == nil || !aggregate.AttachmentRevision.ExpiresAt.After(now) {
		return ImageAsset{}, ErrNotFound
	}
	for _, asset := range aggregate.AttachmentRevision.Assets {
		if asset.AssetIndex == assetIndex && asset.ExpiresAt.After(now) {
			return cloneImageAsset(asset), nil
		}
	}
	return ImageAsset{}, ErrNotFound
}

func (tx *attachmentTx) LatestPasteAggregate(_ context.Context, workspaceID string, _ time.Time) (PasteAggregate, error) {
	tx.latestAggregateCalls++
	var latest PasteAggregate
	found := false
	prefix := workspaceID + "\x00"
	for key, aggregate := range tx.aggregates {
		if !strings.HasPrefix(key, prefix) || aggregate.Deleted {
			continue
		}
		if !found || aggregate.ServerSequence > latest.ServerSequence {
			latest = clonePasteAggregate(aggregate)
			found = true
		}
	}
	if !found {
		return PasteAggregate{}, ErrNotFound
	}
	return latest, nil
}

func (tx *attachmentTx) TouchPaste(_ context.Context, _, _ string, _ time.Time) error {
	tx.touchPasteCalls++
	return nil
}

func (tx *attachmentTx) ListPastes(context.Context, string, time.Time, time.Time) ([]TextRevision, error) {
	tx.legacyListCalls++
	return nil, errAttachmentTest
}

func (tx *attachmentTx) Snapshot(context.Context, string, time.Time) (SnapshotResult, error) {
	tx.legacySnapshotCalls++
	return SnapshotResult{}, errAttachmentTest
}

func (tx *attachmentTx) LatestPaste(context.Context, string, time.Time) (LatestPaste, error) {
	tx.latestPasteCalls++
	return LatestPaste{}, errAttachmentTest
}

type trackingAttachmentImageStore struct {
	real           *images.FileStore
	store          *attachmentStore
	putCalls       int
	openCalls      int
	removes        []images.StoredAsset
	putAssets      []images.StoredAsset
	putRevisionIDs []string
	putDuringTx    []bool
	openDuringTx   []bool
	removeDuringTx []bool
	removeTrees    []attachmentRemoveTreeCall
	removeTreeErr  error
	failPutAt      int
	openErr        error
}

type attachmentRemoveTreeCall struct {
	workspaceID string
	pasteID     string
	revisionID  string
	duringTx    bool
}

func (s *trackingAttachmentImageStore) Put(workspaceID, pasteID, revisionID string, index int, plaintext []byte) (images.StoredAsset, error) {
	s.putCalls++
	s.putRevisionIDs = append(s.putRevisionIDs, revisionID)
	s.putDuringTx = append(s.putDuringTx, s.store.inTx)
	s.store.events = append(s.store.events, "image:put")
	if s.failPutAt > 0 && s.putCalls == s.failPutAt {
		return images.StoredAsset{}, errAttachmentTest
	}
	asset, err := s.real.Put(workspaceID, pasteID, revisionID, index, plaintext)
	if err == nil {
		s.putAssets = append(s.putAssets, asset)
	}
	return asset, err
}

func (s *trackingAttachmentImageStore) Open(asset images.StoredAsset) ([]byte, error) {
	s.openCalls++
	s.openDuringTx = append(s.openDuringTx, s.store.inTx)
	s.store.events = append(s.store.events, "image:open")
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.real.Open(asset)
}

func (s *trackingAttachmentImageStore) Remove(asset images.StoredAsset) error {
	s.removes = append(s.removes, asset)
	s.removeDuringTx = append(s.removeDuringTx, s.store.inTx)
	s.store.events = append(s.store.events, "image:remove")
	return s.real.Remove(asset)
}

func (s *trackingAttachmentImageStore) RemoveTree(workspaceID, pasteID, revisionID string) error {
	s.removeTrees = append(s.removeTrees, attachmentRemoveTreeCall{
		workspaceID: workspaceID, pasteID: pasteID, revisionID: revisionID, duringTx: s.store.inTx,
	})
	s.store.events = append(s.store.events, "image:remove-tree")
	if s.removeTreeErr != nil {
		return s.removeTreeErr
	}
	return s.real.RemoveTree(workspaceID, pasteID, revisionID)
}

func (s *trackingAttachmentImageStore) RemovePaste(workspaceID, pasteID string) error {
	return s.real.RemovePaste(workspaceID, pasteID)
}

type attachmentHarness struct {
	service    *Service
	store      *attachmentStore
	tx         *attachmentTx
	imageStore *trackingAttachmentImageStore
	keyring    *secure.Keyring
	random     *attachmentRandom
	now        time.Time
}

func newAttachmentHarness(t *testing.T) *attachmentHarness {
	t.Helper()
	now := time.Date(2026, 8, 14, 3, 4, 5, 987654321, time.FixedZone("KST", 9*60*60))
	random := &attachmentRandom{next: 1}
	keyring, err := secure.NewKeyring("attachment-test", map[string][]byte{
		"attachment-test": bytes.Repeat([]byte{0x5a}, 32),
	}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	realStore, err := images.NewFileStore(t.TempDir(), keyring)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tx := newAttachmentTx()
	store := &attachmentStore{tx: tx}
	tx.events = &store.events
	imageStore := &trackingAttachmentImageStore{real: realStore, store: store}
	service := NewService(store, keyring, random, attachmentClock{now: now})
	service.SetImageStore(imageStore)
	return &attachmentHarness{
		service: service, store: store, tx: tx, imageStore: imageStore,
		keyring: keyring, random: random, now: now,
	}
}

func (h *attachmentHarness) seedText(t *testing.T, text string) {
	t.Helper()
	expiresAt := h.now.AddDate(1, 0, 0)
	envelope, err := h.keyring.Encrypt(
		"paste-text",
		textObjectID(attachmentWorkspaceID, attachmentPasteID, attachmentTextID),
		[]byte(text),
	)
	if err != nil {
		t.Fatalf("encrypt seed text: %v", err)
	}
	revision := TextRevision{
		WorkspaceID: attachmentWorkspaceID, PasteID: attachmentPasteID,
		RevisionID: attachmentTextID, RevisionKind: RevisionContent,
		ServerSequence: 4, CreatedAt: h.now.Add(-time.Hour), ExpiresAt: expiresAt,
		Envelope: envelope,
	}
	h.tx.nextSeq = revision.ServerSequence
	h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)] = PasteAggregate{
		PasteID: attachmentPasteID, RevisionID: attachmentTextID,
		ServerSequence: revision.ServerSequence, CreatedAt: revision.CreatedAt,
		TextExpiresAt: expiresAt, TextRevision: cloneTextRevision(&revision),
	}
}

func attachmentPrincipal(scope string) Principal {
	return Principal{WorkspaceID: attachmentWorkspaceID, DeviceID: attachmentDeviceID, Scope: scope}
}

func attachmentBMP(seed int) images.AssetInput {
	width := seed + 1
	height := seed + 2
	value := make([]byte, 27+seed)
	copy(value[:2], []byte("BM"))
	binary.LittleEndian.PutUint32(value[18:22], uint32(width))
	binary.LittleEndian.PutUint32(value[22:26], uint32(height))
	for index := 26; index < len(value); index++ {
		value[index] = byte(seed + index)
	}
	return images.AssetInput{MIMEType: "image/bmp", Width: width, Height: height, Bytes: value}
}

func decodePasteResult(t *testing.T, result Result) PasteResponse {
	t.Helper()
	var response PasteResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		t.Fatalf("decode PasteResponse: %v; body = %q", err, result.Body)
	}
	return response
}

func aggregateMapKey(workspaceID, pasteID string) string {
	return workspaceID + "\x00" + pasteID
}

func idempotencyRecordKey(scopeID, operation string, keyHash []byte) string {
	return scopeID + "\x00" + operation + "\x00" + string(keyHash)
}

func cloneIdempotencyRecord(record IdempotencyRecord) IdempotencyRecord {
	record.KeyHash = bytes.Clone(record.KeyHash)
	record.RequestHash = bytes.Clone(record.RequestHash)
	record.Response.Envelope = cloneEnvelope(record.Response.Envelope)
	return record
}

func cloneEnvelope(envelope secure.Envelope) secure.Envelope {
	envelope.Nonce = bytes.Clone(envelope.Nonce)
	envelope.Ciphertext = bytes.Clone(envelope.Ciphertext)
	return envelope
}

func cloneImageAsset(asset ImageAsset) ImageAsset {
	asset.Envelope = cloneEnvelope(asset.Envelope)
	asset.Bytes = bytes.Clone(asset.Bytes)
	return asset
}

func cloneTextRevision(revision *TextRevision) *TextRevision {
	if revision == nil {
		return nil
	}
	result := *revision
	result.Envelope = cloneEnvelope(revision.Envelope)
	if revision.Assets != nil {
		result.Assets = make([]ImageAsset, len(revision.Assets))
		for index, asset := range revision.Assets {
			result.Assets[index] = cloneImageAsset(asset)
		}
	}
	return &result
}

func clonePasteAggregate(aggregate PasteAggregate) PasteAggregate {
	aggregate.TextRevision = cloneTextRevision(aggregate.TextRevision)
	aggregate.AttachmentRevision = cloneTextRevision(aggregate.AttachmentRevision)
	return aggregate
}

func clonePasteAggregates(aggregates []PasteAggregate) []PasteAggregate {
	result := make([]PasteAggregate, len(aggregates))
	for index, aggregate := range aggregates {
		result[index] = clonePasteAggregate(aggregate)
	}
	return result
}

func TestReplaceAttachmentsRejectsScopeAndInvalidInputBeforeMutation(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "unchanged")
	valid := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(0)}}

	for _, scope := range []string{"connector", ""} {
		_, err := h.service.ReplaceAttachments(
			context.Background(), attachmentPrincipal(scope), attachmentPasteID,
			"00000000-0000-4000-8000-000000000b01", valid,
		)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("ReplaceAttachments() scope %q error = %v, want ErrForbidden", scope, err)
		}
	}

	_, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), "not-a-paste",
		"00000000-0000-4000-8000-000000000b02", valid,
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReplaceAttachments() invalid paste error = %v, want ErrInvalid", err)
	}

	_, err = h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"not-an-idempotency-key", valid,
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReplaceAttachments() invalid idempotency error = %v, want ErrInvalid", err)
	}

	readsBeforeValidation := h.random.reads
	nine := make([]images.AssetInput, images.MaxAttachmentItems+1)
	for index := range nine {
		nine[index] = attachmentBMP(index)
	}
	_, err = h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b03", ReplaceAttachmentsInput{Assets: nine},
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReplaceAttachments() nine assets error = %v, want ErrInvalid", err)
	}
	invalidImage := ReplaceAttachmentsInput{Assets: []images.AssetInput{{
		MIMEType: "image/png", Width: 1, Height: 1, Bytes: []byte("not a png"),
	}}}
	_, err = h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b04", invalidImage,
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReplaceAttachments() invalid image error = %v, want ErrInvalid", err)
	}
	if h.random.reads != readsBeforeValidation {
		t.Fatalf("invalid attachment bundle consumed randomness: reads %d -> %d", readsBeforeValidation, h.random.reads)
	}
	if h.store.withinTxCalls != 0 || h.imageStore.putCalls != 0 || h.tx.appendAttachmentCalls != 0 {
		t.Fatalf("rejected inputs reached mutation: tx=%d put=%d append=%d", h.store.withinTxCalls, h.imageStore.putCalls, h.tx.appendAttachmentCalls)
	}
}

func TestReplaceAttachmentsAcceptsEightAndExplicitClearPreservingExactText(t *testing.T) {
	h := newAttachmentHarness(t)
	exactText := "  line one\r\nline two\ntrailing spaces  "
	h.seedText(t, exactText)
	assets := make([]images.AssetInput, images.MaxAttachmentItems)
	for index := range assets {
		assets[index] = attachmentBMP(index)
	}

	result, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b11", ReplaceAttachmentsInput{Assets: assets},
	)
	if err != nil {
		t.Fatalf("ReplaceAttachments(eight) error = %v", err)
	}
	response := decodePasteResult(t, result)
	if result.Status != 200 || response.Text == nil || *response.Text != exactText {
		t.Fatalf("ReplaceAttachments(eight) status/text = %d/%#v", result.Status, response.Text)
	}
	if response.Kind != RevisionContent || response.AttachmentRevisionID == "" || response.RevisionID != response.AttachmentRevisionID {
		t.Fatalf("ReplaceAttachments(eight) aggregate metadata = %#v", response)
	}
	if len(response.Assets) != images.MaxAttachmentItems {
		t.Fatalf("ReplaceAttachments(eight) assets = %d, want %d", len(response.Assets), images.MaxAttachmentItems)
	}
	wantExpiry := h.now.Add(ImageLifetime).UTC().Truncate(time.Second)
	for index, asset := range response.Assets {
		input := assets[index]
		if asset.AssetIndex != index || asset.MIMEType != input.MIMEType || asset.Width != input.Width || asset.Height != input.Height || asset.ByteSize != int64(len(input.Bytes)) || !asset.ExpiresAt.Equal(wantExpiry) {
			t.Fatalf("response asset %d = %#v, input = %#v", index, asset, input)
		}
	}
	if h.imageStore.putCalls != images.MaxAttachmentItems || h.tx.appendAttachmentCalls != 1 || h.tx.appendTextCalls != 0 || h.tx.setPasteKindCalls != 0 {
		t.Fatalf("replacement work = put:%d attachment:%d text:%d kind:%d", h.imageStore.putCalls, h.tx.appendAttachmentCalls, h.tx.appendTextCalls, h.tx.setPasteKindCalls)
	}

	putCalls := h.imageStore.putCalls
	appendCalls := h.tx.appendAttachmentCalls
	nine := append(append([]images.AssetInput(nil), assets...), attachmentBMP(9))
	_, err = h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b12", ReplaceAttachmentsInput{Assets: nine},
	)
	if !errors.Is(err, ErrInvalid) || h.imageStore.putCalls != putCalls || h.tx.appendAttachmentCalls != appendCalls {
		t.Fatalf("ReplaceAttachments(nine) = %v, put:%d append:%d", err, h.imageStore.putCalls, h.tx.appendAttachmentCalls)
	}

	cleared, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b13", ReplaceAttachmentsInput{},
	)
	if err != nil {
		t.Fatalf("ReplaceAttachments(clear) error = %v", err)
	}
	clearResponse := decodePasteResult(t, cleared)
	if clearResponse.AttachmentRevisionID == "" || len(clearResponse.Assets) != 0 || clearResponse.Text == nil || *clearResponse.Text != exactText {
		t.Fatalf("ReplaceAttachments(clear) response = %#v", clearResponse)
	}
	if h.imageStore.putCalls != putCalls || h.tx.appendAttachmentCalls != appendCalls+1 || h.tx.appendTextCalls != 0 {
		t.Fatalf("clear work = put:%d attachment:%d text:%d", h.imageStore.putCalls, h.tx.appendAttachmentCalls, h.tx.appendTextCalls)
	}
	if len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("successful empty clear removed revision trees: %#v", h.imageStore.removeTrees)
	}
	var clearJSON map[string]any
	if err := json.Unmarshal(cleared.Body, &clearJSON); err != nil {
		t.Fatal(err)
	}
	if clearJSON["attachment_revision_id"] == "" {
		t.Fatalf("clear JSON omitted attachment_revision_id: %s", cleared.Body)
	}
	if encodedAssets, present := clearJSON["assets"]; present {
		items, ok := encodedAssets.([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("clear JSON assets = %#v, want omitted or empty", encodedAssets)
		}
	}
}

func TestReplaceAttachmentsReplayIsStableAndCanonicalIncludesOrderedDigests(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "stable")
	input := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(1), attachmentBMP(2)}}
	key := "00000000-0000-4000-8000-000000000b21"

	first, err := h.service.ReplaceAttachments(context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input)
	if err != nil {
		t.Fatalf("first ReplaceAttachments() error = %v", err)
	}
	putCalls := h.imageStore.putCalls
	appendCalls := h.tx.appendAttachmentCalls
	aggregateCalls := h.tx.pasteAggregateCalls
	randomReads := h.random.reads
	lockCalls := h.tx.lockCalls
	withinTxCalls := h.store.withinTxCalls
	second, err := h.service.ReplaceAttachments(context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input)
	if err != nil {
		t.Fatalf("replayed ReplaceAttachments() error = %v", err)
	}
	if first.Status != second.Status || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("replay response = %d/%d %q/%q", first.Status, second.Status, first.Body, second.Body)
	}
	if h.imageStore.putCalls != putCalls || h.tx.appendAttachmentCalls != appendCalls || h.tx.pasteAggregateCalls != aggregateCalls {
		t.Fatalf("replay duplicated work: put %d/%d append %d/%d aggregate %d/%d", putCalls, h.imageStore.putCalls, appendCalls, h.tx.appendAttachmentCalls, aggregateCalls, h.tx.pasteAggregateCalls)
	}
	if h.random.reads != randomReads || h.tx.lockCalls != lockCalls || h.store.withinTxCalls != withinTxCalls+1 {
		t.Fatalf("replay bypassed preflight: random=%d/%d lock=%d/%d tx=%d/%d", randomReads, h.random.reads, lockCalls, h.tx.lockCalls, withinTxCalls+1, h.store.withinTxCalls)
	}
	wantOperation := "paste.attachments.replace:" + attachmentPasteID
	if h.tx.lastLockedOperation != wantOperation {
		t.Fatalf("idempotency operation = %q, want %q", h.tx.lastLockedOperation, wantOperation)
	}

	changed := ReplaceAttachmentsInput{Assets: append([]images.AssetInput(nil), input.Assets...)}
	changed.Assets[0].Bytes = bytes.Clone(changed.Assets[0].Bytes)
	changed.Assets[0].Bytes[len(changed.Assets[0].Bytes)-1] ^= 0xff
	_, err = h.service.ReplaceAttachments(context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, changed)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed bytes replay error = %v, want ErrIdempotencyConflict", err)
	}
	if h.imageStore.putCalls != putCalls || h.tx.appendAttachmentCalls != appendCalls {
		t.Fatalf("conflicting replay duplicated work: put=%d append=%d", h.imageStore.putCalls, h.tx.appendAttachmentCalls)
	}
}

func TestReplaceAttachmentsPreparesFilesBeforeMutationTransaction(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "transaction boundary")
	_, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b22",
		ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(0), attachmentBMP(1)}},
	)
	if err != nil {
		t.Fatalf("ReplaceAttachments() error = %v", err)
	}
	if h.store.withinTxCalls != 2 {
		t.Fatalf("ReplaceAttachments() transactions = %d, want preflight + mutation", h.store.withinTxCalls)
	}
	for index, duringTx := range h.imageStore.putDuringTx {
		if duringTx {
			t.Fatalf("imageStore.Put call %d ran inside a transaction", index)
		}
	}
	if len(h.imageStore.openDuringTx) != 0 || len(h.imageStore.removeDuringTx) != 0 || len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("successful mutation image I/O = open:%#v remove:%#v trees:%#v", h.imageStore.openDuringTx, h.imageStore.removeDuringTx, h.imageStore.removeTrees)
	}
	wantEvents := []string{
		"tx:start", "tx:end",
		"image:put", "image:put",
		"tx:start", "idempotency:lock", "tx:end",
	}
	if !equalAttachmentStrings(h.store.events, wantEvents) {
		t.Fatalf("ReplaceAttachments() event order = %#v, want %#v", h.store.events, wantEvents)
	}
}

func TestReplaceAttachmentsCleansPreparedFilesWhenMutationFindsRacingReplay(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "racing replay")
	input := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(2), attachmentBMP(3)}}
	key := "00000000-0000-4000-8000-000000000b23"
	first, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input,
	)
	if err != nil {
		t.Fatalf("first ReplaceAttachments() error = %v", err)
	}
	if len(h.tx.records) != 1 {
		t.Fatalf("idempotency records = %d, want 1", len(h.tx.records))
	}
	var recordKey string
	var replayRecord IdempotencyRecord
	for key, record := range h.tx.records {
		recordKey = key
		replayRecord = cloneIdempotencyRecord(record)
	}
	delete(h.tx.records, recordKey)
	h.tx.lockInjection = &replayRecord

	committedFileCount := len(h.imageStore.putAssets)
	removeTreeCount := len(h.imageStore.removeTrees)
	appendCalls := h.tx.appendAttachmentCalls
	second, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input,
	)
	if err != nil {
		t.Fatalf("racing replay ReplaceAttachments() error = %v", err)
	}
	if first.Status != second.Status || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("racing replay response = %d/%d %q/%q", first.Status, second.Status, first.Body, second.Body)
	}
	if h.tx.appendAttachmentCalls != appendCalls {
		t.Fatalf("racing replay executed mutation callback: append=%d/%d", appendCalls, h.tx.appendAttachmentCalls)
	}
	prepared := h.imageStore.putAssets[committedFileCount:]
	if len(prepared) != len(input.Assets) || len(h.imageStore.removeTrees)-removeTreeCount != 1 || len(h.imageStore.removes) != 0 {
		t.Fatalf("racing replay prepared/tree/per-asset cleanup = %d/%d/%d", len(prepared), len(h.imageStore.removeTrees)-removeTreeCount, len(h.imageStore.removes))
	}
	cleanup := h.imageStore.removeTrees[removeTreeCount]
	if cleanup.workspaceID != attachmentWorkspaceID || cleanup.pasteID != attachmentPasteID || cleanup.revisionID != h.imageStore.putRevisionIDs[committedFileCount] || cleanup.duringTx {
		t.Fatalf("racing replay RemoveTree() = %#v", cleanup)
	}
	for index, stored := range prepared {
		if _, openErr := h.imageStore.real.Open(stored); openErr == nil {
			t.Fatalf("racing replay prepared file %d remains", index)
		}
	}
	for index, stored := range h.imageStore.putAssets[:committedFileCount] {
		if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
			t.Fatalf("committed file %d removed by racing replay: %v", index, openErr)
		}
	}
}

func TestReplaceAttachmentsResolvesAppliedCommitErrorFromIdempotencyRecord(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "ambiguous commit")
	h.store.appliedErr = errAttachmentTest
	h.store.appliedErrAt = 2
	input := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(4), attachmentBMP(5)}}

	result, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b24", input,
	)
	if err != nil {
		t.Fatalf("ReplaceAttachments() ambiguous commit error = %v", err)
	}
	response := decodePasteResult(t, result)
	if result.Status != 200 || response.AttachmentRevisionID == "" || len(response.Assets) != len(input.Assets) {
		t.Fatalf("ReplaceAttachments() ambiguous replay = %d/%#v", result.Status, response)
	}
	if h.store.withinTxCalls != 3 || len(h.tx.records) != 1 {
		t.Fatalf("ambiguous commit verification = tx:%d records:%d", h.store.withinTxCalls, len(h.tx.records))
	}
	if len(h.imageStore.removes) != 0 || len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("verified committed files were removed: assets=%#v trees=%#v", h.imageStore.removes, h.imageStore.removeTrees)
	}
	for index, stored := range h.imageStore.putAssets {
		if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
			t.Fatalf("verified committed file %d unavailable: %v", index, openErr)
		}
	}
}

func TestReplaceAttachmentsRetainsFilesWhenCommittedRecordCannotDecode(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "committed decode failure")
	h.store.appliedErr = errAttachmentTest
	h.store.appliedErrAt = 2
	h.store.afterApplied = func(tx *attachmentTx) {
		for key, record := range tx.records {
			record.RequestHash = []byte("different request")
			tx.records[key] = record
		}
	}

	_, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b25",
		ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(6)}},
	)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("ReplaceAttachments() committed decode error = %v, want ErrIdempotencyConflict", err)
	}
	if h.store.withinTxCalls != 3 || len(h.tx.records) != 1 || len(h.imageStore.removes) != 0 || len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("committed decode failure state = tx:%d records:%d removes:%d trees:%d", h.store.withinTxCalls, len(h.tx.records), len(h.imageStore.removes), len(h.imageStore.removeTrees))
	}
	for index, stored := range h.imageStore.putAssets {
		if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
			t.Fatalf("committed undecodable file %d unavailable: %v", index, openErr)
		}
	}
}

func TestReplaceAttachmentsRetainsFilesWhenCommitVerificationFails(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "unknown commit")
	h.store.commitErr = errAttachmentTest
	h.store.commitErrAt = 2
	h.store.readErr = errAttachmentVerification
	h.store.readErrAt = 3

	_, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000b26",
		ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(7)}},
	)
	if !errors.Is(err, errAttachmentTest) || !errors.Is(err, errAttachmentVerification) {
		t.Fatalf("ReplaceAttachments() unknown commit error = %v", err)
	}
	if h.store.withinTxCalls != 3 || len(h.tx.records) != 0 || len(h.imageStore.removes) != 0 || len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("unknown commit state = tx:%d records:%d removes:%d trees:%d", h.store.withinTxCalls, len(h.tx.records), len(h.imageStore.removes), len(h.imageStore.removeTrees))
	}
	for index, stored := range h.imageStore.putAssets {
		if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
			t.Fatalf("unknown-outcome file %d unavailable: %v", index, openErr)
		}
	}
}

func equalAttachmentStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestReplaceAttachmentsTreatsEachBundleAsCompleteAndKeepsSuccessfulRevisionFiles(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "bundle text")
	ctx := context.Background()
	principal := attachmentPrincipal("full")

	firstInputs := []images.AssetInput{attachmentBMP(0), attachmentBMP(1), attachmentBMP(2)}
	first, err := h.service.ReplaceAttachments(
		ctx, principal, attachmentPasteID, "00000000-0000-4000-8000-000000000b31",
		ReplaceAttachmentsInput{Assets: firstInputs},
	)
	if err != nil {
		t.Fatalf("first ReplaceAttachments() error = %v", err)
	}
	assertResponseAssetWidths(t, decodePasteResult(t, first), []int{1, 2, 3})

	reorderedInputs := []images.AssetInput{firstInputs[2], firstInputs[0]}
	reordered, err := h.service.ReplaceAttachments(
		ctx, principal, attachmentPasteID, "00000000-0000-4000-8000-000000000b32",
		ReplaceAttachmentsInput{Assets: reorderedInputs},
	)
	if err != nil {
		t.Fatalf("reordered ReplaceAttachments() error = %v", err)
	}
	assertResponseAssetWidths(t, decodePasteResult(t, reordered), []int{3, 1})

	removed, err := h.service.ReplaceAttachments(
		ctx, principal, attachmentPasteID, "00000000-0000-4000-8000-000000000b33",
		ReplaceAttachmentsInput{Assets: reorderedInputs[:1]},
	)
	if err != nil {
		t.Fatalf("removing ReplaceAttachments() error = %v", err)
	}
	removeResponse := decodePasteResult(t, removed)
	assertResponseAssetWidths(t, removeResponse, []int{3})
	currentAsset, currentBytes, err := h.service.AttachmentAsset(ctx, principal, attachmentPasteID, 0)
	if err != nil {
		t.Fatalf("AttachmentAsset(current) error = %v", err)
	}
	if currentAsset.RevisionID != removeResponse.AttachmentRevisionID || !bytes.Equal(currentBytes, reorderedInputs[0].Bytes) {
		t.Fatalf("current asset = %#v/%x", currentAsset, currentBytes)
	}

	cleared, err := h.service.ReplaceAttachments(
		ctx, principal, attachmentPasteID, "00000000-0000-4000-8000-000000000b34",
		ReplaceAttachmentsInput{},
	)
	if err != nil {
		t.Fatalf("clear ReplaceAttachments() error = %v", err)
	}
	if response := decodePasteResult(t, cleared); response.AttachmentRevisionID == "" || len(response.Assets) != 0 {
		t.Fatalf("clear response = %#v", response)
	}
	if _, _, err := h.service.AttachmentAsset(ctx, principal, attachmentPasteID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AttachmentAsset() after clear error = %v, want ErrNotFound", err)
	}

	if len(h.imageStore.removes) != 0 || len(h.imageStore.removeTrees) != 0 {
		t.Fatalf("successful replacements removed immutable files: assets=%#v trees=%#v", h.imageStore.removes, h.imageStore.removeTrees)
	}
	for index, stored := range h.imageStore.putAssets {
		if _, err := h.imageStore.real.Open(stored); err != nil {
			t.Fatalf("successful revision file %d unavailable: %v", index, err)
		}
	}
}

func assertResponseAssetWidths(t *testing.T, response PasteResponse, widths []int) {
	t.Helper()
	if len(response.Assets) != len(widths) {
		t.Fatalf("response assets = %#v, want widths %#v", response.Assets, widths)
	}
	for index, width := range widths {
		if response.Assets[index].AssetIndex != index || response.Assets[index].Width != width {
			t.Fatalf("response asset %d = %#v, want index/width %d/%d", index, response.Assets[index], index, width)
		}
	}
}

func TestReplaceAttachmentsRemovesOnlyAttemptedRevisionFilesOnEveryMutationFailure(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*attachmentHarness)
	}{
		{
			name: "second write",
			setup: func(h *attachmentHarness) {
				h.imageStore.failPutAt = 2
			},
		},
		{
			name: "append",
			setup: func(h *attachmentHarness) {
				h.tx.appendAttachmentErr = errAttachmentTest
			},
		},
		{
			name: "aggregate read",
			setup: func(h *attachmentHarness) {
				h.tx.aggregateErr = errAttachmentTest
			},
		},
		{
			name: "text decryption",
			setup: func(h *attachmentHarness) {
				aggregate := h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)]
				aggregate.TextRevision.Envelope = secure.Envelope{KeyID: "missing-key"}
				h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)] = aggregate
			},
		},
		{
			name: "idempotency write",
			setup: func(h *attachmentHarness) {
				h.tx.putIdempotencyErr = errAttachmentTest
			},
		},
		{
			name: "transaction commit",
			setup: func(h *attachmentHarness) {
				h.store.commitErr = errAttachmentTest
				h.store.commitErrAt = 2
			},
		},
	}

	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newAttachmentHarness(t)
			h.seedText(t, "rollback text")
			oldStored, err := h.imageStore.real.Put(
				attachmentWorkspaceID, attachmentPasteID, attachmentOldID, 0, []byte("old immutable revision"),
			)
			if err != nil {
				t.Fatalf("seed old image: %v", err)
			}
			setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, attachmentOldID, h.now.Add(-time.Minute), []ImageAsset{{
				AssetIndex: 0, MIMEType: "image/bmp", Width: 1, Height: 2, ByteSize: 22,
				ExpiresAt: h.now.Add(ImageLifetime), StorageKey: oldStored.StorageKey, Envelope: oldStored.Envelope,
			}})
			test.setup(h)

			key := fmt.Sprintf("00000000-0000-4000-8000-%012d", 1200+testIndex)
			_, err = h.service.ReplaceAttachments(
				context.Background(), attachmentPrincipal("full"), attachmentPasteID, key,
				ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(3), attachmentBMP(4)}},
			)
			if err == nil {
				t.Fatal("ReplaceAttachments() error = nil")
			}
			if h.tx.appendTextCalls != 0 {
				t.Fatalf("failed replacement appended text revisions: %d", h.tx.appendTextCalls)
			}
			if len(h.imageStore.removes) != 0 || len(h.imageStore.removeTrees) != 1 {
				t.Fatalf("cleanup per-asset/trees = %d/%d", len(h.imageStore.removes), len(h.imageStore.removeTrees))
			}
			cleanup := h.imageStore.removeTrees[0]
			if cleanup.workspaceID != attachmentWorkspaceID || cleanup.pasteID != attachmentPasteID || cleanup.revisionID != h.imageStore.putRevisionIDs[0] || cleanup.duringTx {
				t.Fatalf("RemoveTree() = %#v", cleanup)
			}
			for index, stored := range h.imageStore.putAssets {
				if _, openErr := h.imageStore.real.Open(stored); openErr == nil {
					t.Fatalf("attempted revision file %d remains after failure", index)
				}
			}
			oldBytes, oldErr := h.imageStore.real.Open(oldStored)
			if oldErr != nil || string(oldBytes) != "old immutable revision" {
				t.Fatalf("old immutable file = %q/%v", oldBytes, oldErr)
			}
		})
	}
}

func TestReplaceAttachmentsPreservesPrimaryErrorsWhenRemoveTreeFails(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*attachmentHarness)
		wantPrimary error
	}{
		{
			name: "partial put",
			setup: func(h *attachmentHarness) {
				h.imageStore.failPutAt = 2
			},
			wantPrimary: ErrUnavailableContent,
		},
		{
			name: "callback rollback",
			setup: func(h *attachmentHarness) {
				h.tx.appendAttachmentErr = errAttachmentTest
			},
			wantPrimary: errAttachmentTest,
		},
	}

	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newAttachmentHarness(t)
			h.seedText(t, "cleanup failure")
			h.imageStore.removeTreeErr = errAttachmentCleanup
			test.setup(h)
			_, err := h.service.ReplaceAttachments(
				context.Background(), attachmentPrincipal("full"), attachmentPasteID,
				fmt.Sprintf("00000000-0000-4000-8000-%012d", 1400+testIndex),
				ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(1), attachmentBMP(2)}},
			)
			if !errors.Is(err, test.wantPrimary) || !errors.Is(err, errAttachmentCleanup) {
				t.Fatalf("ReplaceAttachments() cleanup error = %v", err)
			}
			if len(h.imageStore.removeTrees) != 1 || len(h.imageStore.removes) != 0 {
				t.Fatalf("cleanup tree/per-asset calls = %d/%d", len(h.imageStore.removeTrees), len(h.imageStore.removes))
			}
			cleanup := h.imageStore.removeTrees[0]
			if cleanup.workspaceID != attachmentWorkspaceID || cleanup.pasteID != attachmentPasteID || cleanup.revisionID != h.imageStore.putRevisionIDs[0] || cleanup.duringTx {
				t.Fatalf("failed RemoveTree() = %#v", cleanup)
			}
			for index, stored := range h.imageStore.putAssets {
				if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
					t.Fatalf("file %d unexpectedly removed after RemoveTree failure: %v", index, openErr)
				}
			}
		})
	}
}

func TestReplaceAttachmentsReturnsCleanupErrorForRacingReplay(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "racing replay cleanup failure")
	input := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(2), attachmentBMP(3)}}
	key := "00000000-0000-4000-8000-000000000b27"
	first, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input,
	)
	if err != nil {
		t.Fatalf("first ReplaceAttachments() error = %v", err)
	}
	var recordKey string
	var replayRecord IdempotencyRecord
	for key, record := range h.tx.records {
		recordKey = key
		replayRecord = cloneIdempotencyRecord(record)
	}
	delete(h.tx.records, recordKey)
	h.tx.lockInjection = &replayRecord
	h.imageStore.removeTreeErr = errAttachmentCleanup
	committedFileCount := len(h.imageStore.putAssets)
	removeTreeCount := len(h.imageStore.removeTrees)
	appendCalls := h.tx.appendAttachmentCalls

	second, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID, key, input,
	)
	if !errors.Is(err, errAttachmentCleanup) {
		t.Fatalf("racing replay cleanup error = %v", err)
	}
	if first.Status != second.Status || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("racing replay cleanup response = %d/%d %q/%q", first.Status, second.Status, first.Body, second.Body)
	}
	if h.tx.appendAttachmentCalls != appendCalls || len(h.imageStore.removeTrees)-removeTreeCount != 1 || len(h.imageStore.removes) != 0 {
		t.Fatalf("racing replay cleanup work = append:%d/%d trees:%d assets:%d", appendCalls, h.tx.appendAttachmentCalls, len(h.imageStore.removeTrees)-removeTreeCount, len(h.imageStore.removes))
	}
	cleanup := h.imageStore.removeTrees[removeTreeCount]
	if cleanup.revisionID != h.imageStore.putRevisionIDs[committedFileCount] || cleanup.duringTx {
		t.Fatalf("racing replay failed RemoveTree() = %#v", cleanup)
	}
	for index, stored := range h.imageStore.putAssets[committedFileCount:] {
		if _, openErr := h.imageStore.real.Open(stored); openErr != nil {
			t.Fatalf("racing replay file %d unexpectedly removed after cleanup failure: %v", index, openErr)
		}
	}
}

func setAttachmentComponent(tx *attachmentTx, workspaceID, pasteID, revisionID string, createdAt time.Time, assets []ImageAsset) {
	expiresAt := createdAt.Add(ImageLifetime)
	copied := make([]ImageAsset, len(assets))
	for index, asset := range assets {
		copied[index] = cloneImageAsset(asset)
		copied[index].WorkspaceID = workspaceID
		copied[index].PasteID = pasteID
		copied[index].RevisionID = revisionID
		if copied[index].ExpiresAt.IsZero() {
			copied[index].ExpiresAt = expiresAt
		}
	}
	revision := TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: RevisionAttachmentBundle, ServerSequence: tx.nextSeq + 1,
		CreatedAt: createdAt, ExpiresAt: expiresAt, Assets: copied,
	}
	tx.nextSeq = revision.ServerSequence
	key := aggregateMapKey(workspaceID, pasteID)
	aggregate := tx.aggregates[key]
	aggregate.PasteID = pasteID
	aggregate.RevisionID = revisionID
	aggregate.AttachmentRevisionID = revisionID
	aggregate.ServerSequence = revision.ServerSequence
	aggregate.CreatedAt = createdAt
	aggregate.AttachmentExpiresAt = expiresAt
	aggregate.AttachmentRevision = cloneTextRevision(&revision)
	tx.aggregates[key] = aggregate
}

func TestAttachmentAssetReadsOnlyTheExactCurrentPasteAsset(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "asset lookup")
	plain := []byte("current attachment bytes")
	stored, err := h.imageStore.real.Put(attachmentWorkspaceID, attachmentPasteID, attachmentOldID, 0, plain)
	if err != nil {
		t.Fatalf("seed current image: %v", err)
	}
	setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, attachmentOldID, h.now, []ImageAsset{{
		AssetIndex: 0, MIMEType: "image/bmp", Width: 1, Height: 2, ByteSize: int64(len(plain)),
		ExpiresAt: h.now.Add(ImageLifetime), StorageKey: stored.StorageKey, Envelope: stored.Envelope,
	}})

	asset, got, err := h.service.AttachmentAsset(context.Background(), attachmentPrincipal("full"), attachmentPasteID, 0)
	if err != nil {
		t.Fatalf("AttachmentAsset() error = %v", err)
	}
	if asset.PasteID != attachmentPasteID || asset.RevisionID != attachmentOldID || !bytes.Equal(got, plain) {
		t.Fatalf("AttachmentAsset() = %#v/%q", asset, got)
	}
	if h.tx.lastCurrentWorkspace != attachmentWorkspaceID || h.tx.lastCurrentPaste != attachmentPasteID || h.tx.lastCurrentIndex != 0 {
		t.Fatalf("CurrentAttachmentAsset args = %q/%q/%d", h.tx.lastCurrentWorkspace, h.tx.lastCurrentPaste, h.tx.lastCurrentIndex)
	}

	currentCalls := h.tx.currentAssetCalls
	openCalls := h.imageStore.openCalls
	invalidCases := []struct {
		name      string
		principal Principal
		pasteID   string
		index     int
		want      error
	}{
		{name: "wrong scope", principal: attachmentPrincipal("connector"), pasteID: attachmentPasteID, index: 0, want: ErrForbidden},
		{name: "invalid paste", principal: attachmentPrincipal("full"), pasteID: "bad", index: 0, want: ErrInvalid},
		{name: "negative index", principal: attachmentPrincipal("full"), pasteID: attachmentPasteID, index: -1, want: ErrInvalid},
		{name: "index at limit", principal: attachmentPrincipal("full"), pasteID: attachmentPasteID, index: images.MaxBundleItems, want: ErrInvalid},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := h.service.AttachmentAsset(context.Background(), test.principal, test.pasteID, test.index)
			if !errors.Is(err, test.want) {
				t.Fatalf("AttachmentAsset() error = %v, want %v", err, test.want)
			}
		})
	}
	if h.tx.currentAssetCalls != currentCalls || h.imageStore.openCalls != openCalls {
		t.Fatalf("invalid lookup reached store/file: current=%d/%d open=%d/%d", currentCalls, h.tx.currentAssetCalls, openCalls, h.imageStore.openCalls)
	}

	crossPrincipal := Principal{WorkspaceID: attachmentOtherWSID, DeviceID: attachmentDeviceID, Scope: "full"}
	if _, _, err := h.service.AttachmentAsset(context.Background(), crossPrincipal, attachmentPasteID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace AttachmentAsset() error = %v", err)
	}
	if _, _, err := h.service.AttachmentAsset(context.Background(), attachmentPrincipal("full"), "00000000-0000-4000-8000-000000000aff", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing AttachmentAsset() error = %v", err)
	}
	if h.imageStore.openCalls != openCalls {
		t.Fatalf("missing/cross-workspace lookup opened a file: %d -> %d", openCalls, h.imageStore.openCalls)
	}

	aggregate := h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)]
	aggregate.AttachmentRevision.ExpiresAt = h.now
	aggregate.AttachmentRevision.Assets[0].ExpiresAt = h.now
	h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)] = aggregate
	if _, _, err := h.service.AttachmentAsset(context.Background(), attachmentPrincipal("full"), attachmentPasteID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired AttachmentAsset() error = %v", err)
	}
	if h.imageStore.openCalls != openCalls {
		t.Fatalf("expired lookup opened a file: %d -> %d", openCalls, h.imageStore.openCalls)
	}

	aggregate.AttachmentRevision.ExpiresAt = h.now.Add(ImageLifetime)
	aggregate.AttachmentRevision.Assets[0].ExpiresAt = h.now.Add(ImageLifetime)
	h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)] = aggregate
	h.imageStore.openErr = errAttachmentTest
	if _, _, err := h.service.AttachmentAsset(context.Background(), attachmentPrincipal("full"), attachmentPasteID, 0); !errors.Is(err, ErrUnavailableContent) {
		t.Fatalf("unavailable AttachmentAsset() error = %v", err)
	}
	if h.tx.latestPasteCalls != 0 {
		t.Fatalf("AttachmentAsset() fell back to LatestPaste %d times", h.tx.latestPasteCalls)
	}
	h.service.SetImageStore(nil)
	currentCalls = h.tx.currentAssetCalls
	if _, _, err := h.service.AttachmentAsset(context.Background(), attachmentPrincipal("full"), attachmentPasteID, 0); !errors.Is(err, ErrUnavailableContent) {
		t.Fatalf("AttachmentAsset() without image store error = %v", err)
	}
	if h.tx.currentAssetCalls != currentCalls {
		t.Fatal("AttachmentAsset() without image store reached CurrentAttachmentAsset")
	}
}

func TestAttachmentAssetDownloadsEveryRetainedLegacyIndex(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "legacy asset lookup")
	legacyRevisionID := "00000000-0000-4000-8000-000000000a07"
	assets := make([]ImageAsset, 0, images.MaxBundleItems-images.MaxAttachmentItems)
	wantBytes := make(map[int][]byte, images.MaxBundleItems-images.MaxAttachmentItems)
	for index := images.MaxAttachmentItems; index < images.MaxBundleItems; index++ {
		plain := []byte(fmt.Sprintf("legacy asset %d", index))
		stored, err := h.imageStore.real.Put(attachmentWorkspaceID, attachmentPasteID, legacyRevisionID, index, plain)
		if err != nil {
			t.Fatalf("seed legacy asset %d: %v", index, err)
		}
		wantBytes[index] = plain
		assets = append(assets, ImageAsset{
			AssetIndex: index, MIMEType: "image/bmp", Width: index + 1, Height: index + 2,
			ByteSize: int64(len(plain)), ExpiresAt: h.now.Add(ImageLifetime),
			StorageKey: stored.StorageKey, Envelope: stored.Envelope,
		})
	}
	setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, legacyRevisionID, h.now, assets)

	for index := images.MaxAttachmentItems; index < images.MaxBundleItems; index++ {
		t.Run(fmt.Sprintf("index_%d", index), func(t *testing.T) {
			asset, content, err := h.service.AttachmentAsset(
				context.Background(), attachmentPrincipal("full"), attachmentPasteID, index,
			)
			if err != nil {
				t.Fatalf("AttachmentAsset(%d) error = %v", index, err)
			}
			if asset.AssetIndex != index || !bytes.Equal(content, wantBytes[index]) {
				t.Fatalf("AttachmentAsset(%d) = %#v/%q", index, asset, content)
			}
		})
	}

	currentCalls := h.tx.currentAssetCalls
	openCalls := h.imageStore.openCalls
	if _, _, err := h.service.AttachmentAsset(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID, images.MaxBundleItems,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AttachmentAsset(%d) error = %v, want ErrInvalid", images.MaxBundleItems, err)
	}
	if h.tx.currentAssetCalls != currentCalls || h.imageStore.openCalls != openCalls {
		t.Fatalf("index %d reached store/file: current=%d/%d open=%d/%d", images.MaxBundleItems, currentCalls, h.tx.currentAssetCalls, openCalls, h.imageStore.openCalls)
	}
}

func TestAggregateResponseCombinesTextAndAttachmentsWithoutSecrets(t *testing.T) {
	h := newAttachmentHarness(t)
	textRevisionID := "00000000-0000-4000-8000-000000000c01"
	attachmentRevisionID := "00000000-0000-4000-8000-000000000c02"
	textExpiresAt := h.now.AddDate(1, 0, 0)
	attachmentExpiresAt := h.now.Add(ImageLifetime)
	envelope, err := h.keyring.Encrypt(
		"paste-text", textObjectID(attachmentWorkspaceID, attachmentPasteID, textRevisionID), nil,
	)
	if err != nil {
		t.Fatalf("encrypt empty text: %v", err)
	}
	textRevision := TextRevision{
		WorkspaceID: attachmentWorkspaceID, PasteID: attachmentPasteID,
		RevisionID: textRevisionID, RevisionKind: RevisionContent,
		ServerSequence: 10, CreatedAt: h.now.Add(-time.Minute),
		ExpiresAt: textExpiresAt, Envelope: envelope,
	}
	attachmentRevision := TextRevision{
		WorkspaceID: attachmentWorkspaceID, PasteID: attachmentPasteID,
		RevisionID: attachmentRevisionID, RevisionKind: RevisionAttachmentBundle,
		ServerSequence: 11, CreatedAt: h.now, ExpiresAt: attachmentExpiresAt,
		Assets: []ImageAsset{
			{
				AssetIndex: 0, MIMEType: "image/bmp", Width: 7, Height: 8, ByteSize: 29,
				ExpiresAt: attachmentExpiresAt, StorageKey: "storage-secret-marker",
				Envelope: secure.Envelope{KeyID: "key-secret-marker", Nonce: []byte("nonce-secret"), Ciphertext: []byte("cipher-secret")},
				Bytes:    []byte("raw-secret-marker"),
			},
			{
				AssetIndex: 1, MIMEType: "image/tiff", Width: 9, Height: 10, ByteSize: 31,
				ExpiresAt: attachmentExpiresAt,
			},
		},
	}
	aggregate := PasteAggregate{
		PasteID: attachmentPasteID, RevisionID: attachmentRevisionID,
		AttachmentRevisionID: attachmentRevisionID, ServerSequence: 11,
		CreatedAt: h.now, TextExpiresAt: textExpiresAt,
		AttachmentExpiresAt: attachmentExpiresAt,
		TextRevision:        &textRevision, AttachmentRevision: &attachmentRevision,
	}

	response, err := h.service.aggregateResponse(context.Background(), aggregate)
	if err != nil {
		t.Fatalf("aggregateResponse() error = %v", err)
	}
	if response.PasteID != aggregate.PasteID || response.RevisionID != aggregate.RevisionID || response.ServerSequence != aggregate.ServerSequence || !response.CreatedAt.Equal(wireTime(aggregate.CreatedAt)) {
		t.Fatalf("aggregate response top-level metadata = %#v", response)
	}
	if response.Kind != RevisionContent || response.Text == nil || *response.Text != "" || !response.ExpiresAt.Equal(wireTime(textExpiresAt)) {
		t.Fatalf("aggregate text response = %#v", response)
	}
	if response.AttachmentRevisionID != attachmentRevisionID || len(response.Assets) != 2 || response.Assets[0].AssetIndex != 0 || response.Assets[1].AssetIndex != 1 || !response.Assets[0].ExpiresAt.Equal(wireTime(attachmentExpiresAt)) {
		t.Fatalf("aggregate attachment response = %#v", response)
	}
	if got := attachmentResponses(attachmentRevision.Assets); len(got) != 2 || got[0].Width != 7 || got[1].Width != 9 {
		t.Fatalf("attachmentResponses() = %#v", got)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal aggregate response: %v", err)
	}
	for _, forbidden := range []string{
		"storage_key", "envelope", "key_id", "nonce", "ciphertext", "bytes",
		"credential", "secret-marker", "nonce-secret", "cipher-secret",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("aggregate response exposed %q: %s", forbidden, encoded)
		}
	}

	legacy := PasteAggregate{
		PasteID:    "00000000-0000-4000-8000-000000000c03",
		RevisionID: attachmentRevisionID, AttachmentRevisionID: attachmentRevisionID,
		ServerSequence: 3, CreatedAt: h.now, AttachmentExpiresAt: attachmentExpiresAt,
		AttachmentRevision: &TextRevision{
			PasteID: "00000000-0000-4000-8000-000000000c03", RevisionID: attachmentRevisionID,
			RevisionKind: RevisionImageBundle, ExpiresAt: attachmentExpiresAt,
			Assets: []ImageAsset{{AssetIndex: 0, MIMEType: "image/bmp", Width: 1, Height: 2, ByteSize: 27, ExpiresAt: attachmentExpiresAt}},
		},
	}
	legacyResponse, err := h.service.aggregateResponse(context.Background(), legacy)
	if err != nil {
		t.Fatalf("aggregateResponse(legacy) error = %v", err)
	}
	if legacyResponse.Text != nil || legacyResponse.Kind != RevisionImageBundle || !legacyResponse.ExpiresAt.Equal(wireTime(attachmentExpiresAt)) || len(legacyResponse.Assets) != 1 {
		t.Fatalf("legacy aggregate response = %#v", legacyResponse)
	}

	emptyAttachmentID := "00000000-0000-4000-8000-000000000c04"
	explicitClear := clonePasteAggregate(aggregate)
	explicitClear.RevisionID = emptyAttachmentID
	explicitClear.AttachmentRevisionID = emptyAttachmentID
	explicitClear.AttachmentRevision.RevisionID = emptyAttachmentID
	explicitClear.AttachmentRevision.Assets = make([]ImageAsset, 0)
	clearResponse, err := h.service.aggregateResponse(context.Background(), explicitClear)
	if err != nil {
		t.Fatalf("aggregateResponse(clear) error = %v", err)
	}
	if clearResponse.AttachmentRevisionID != emptyAttachmentID || clearResponse.Assets == nil || len(clearResponse.Assets) != 0 || clearResponse.Text == nil {
		t.Fatalf("explicit-clear aggregate response = %#v", clearResponse)
	}

	deleted := clonePasteAggregate(aggregate)
	deleted.RevisionID = "00000000-0000-4000-8000-000000000c05"
	deleted.Deleted = true
	deletedResponse, err := h.service.aggregateResponse(context.Background(), deleted)
	if err != nil {
		t.Fatalf("aggregateResponse(deleted) error = %v", err)
	}
	if !deletedResponse.Deleted || deletedResponse.Kind != RevisionTombstone || deletedResponse.Text != nil || deletedResponse.AttachmentRevisionID != "" || len(deletedResponse.Assets) != 0 {
		t.Fatalf("deleted aggregate response = %#v", deletedResponse)
	}
}

func TestPasteMutationsReturnAggregatesAndTextUpdateDoesNotWriteImages(t *testing.T) {
	t.Run("create fetches aggregate", func(t *testing.T) {
		h := newAttachmentHarness(t)
		result, err := h.service.CreatePaste(
			context.Background(), attachmentPrincipal("full"),
			"00000000-0000-4000-8000-000000000c11",
			CreatePasteInput{Text: "created text"},
		)
		if err != nil {
			t.Fatalf("CreatePaste() error = %v", err)
		}
		response := decodePasteResult(t, result)
		if response.Text == nil || *response.Text != "created text" || h.tx.pasteAggregateCalls != 1 {
			t.Fatalf("CreatePaste() aggregate response/calls = %#v/%d", response, h.tx.pasteAggregateCalls)
		}
	})

	t.Run("update preserves attachment component", func(t *testing.T) {
		h := newAttachmentHarness(t)
		h.seedText(t, "old text")
		expiresAt := h.now.Add(ImageLifetime)
		setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, attachmentOldID, h.now.Add(-time.Minute), []ImageAsset{{
			AssetIndex: 0, MIMEType: "image/bmp", Width: 4, Height: 5, ByteSize: 30,
			ExpiresAt: expiresAt, StorageKey: "must-not-open-or-write",
		}})
		exact := " updated\r\ntext\n  "
		result, err := h.service.UpdatePaste(
			context.Background(), attachmentPrincipal("full"), attachmentPasteID,
			"00000000-0000-4000-8000-000000000c12", UpdatePasteInput{Text: exact},
		)
		if err != nil {
			t.Fatalf("UpdatePaste() error = %v", err)
		}
		response := decodePasteResult(t, result)
		if response.Text == nil || *response.Text != exact || response.AttachmentRevisionID != attachmentOldID || len(response.Assets) != 1 || response.Assets[0].Width != 4 {
			t.Fatalf("UpdatePaste() aggregate response = %#v", response)
		}
		if h.tx.pasteAggregateCalls != 1 || h.tx.appendTextCalls != 1 || h.tx.appendAttachmentCalls != 0 || h.imageStore.putCalls != 0 {
			t.Fatalf("UpdatePaste() work = aggregate:%d text:%d attachment:%d image:%d", h.tx.pasteAggregateCalls, h.tx.appendTextCalls, h.tx.appendAttachmentCalls, h.imageStore.putCalls)
		}
	})
}

func TestLatestPasteReturnsExactTextAndOrderedAttachments(t *testing.T) {
	h := newAttachmentHarness(t)
	exact := "  latest line one\r\nline two\ntrailing  "
	h.seedText(t, exact)
	inputs := []images.AssetInput{attachmentBMP(1), attachmentBMP(2)}
	assets := make([]ImageAsset, 0, len(inputs))
	for index, input := range inputs {
		stored, err := h.imageStore.real.Put(
			attachmentWorkspaceID, attachmentPasteID, attachmentOldID, index, input.Bytes,
		)
		if err != nil {
			t.Fatalf("seed attachment %d: %v", index, err)
		}
		assets = append(assets, ImageAsset{
			AssetIndex: index, MIMEType: input.MIMEType, Width: input.Width, Height: input.Height,
			ByteSize: int64(len(input.Bytes)), ExpiresAt: h.now.Add(ImageLifetime),
			StorageKey: stored.StorageKey, Envelope: stored.Envelope,
		})
	}
	setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, attachmentOldID, h.now, assets)

	latest, err := h.service.LatestPaste(context.Background(), identityConnectorPrincipal())
	if err != nil {
		t.Fatalf("LatestPaste() error = %v", err)
	}
	if !latest.Available || latest.PasteID != attachmentPasteID || latest.RevisionID != attachmentOldID || latest.Text != exact || len(latest.Images) != 2 {
		t.Fatalf("LatestPaste() = %#v", latest)
	}
	for index, image := range latest.Images {
		if image.AssetIndex != index || image.MIMEType != inputs[index].MIMEType || !bytes.Equal(image.Bytes, inputs[index].Bytes) {
			t.Fatalf("latest image %d = %#v", index, image)
		}
	}
	if h.tx.latestAggregateCalls != 1 || h.tx.latestPasteCalls != 0 || h.tx.touchPasteCalls != 1 || h.imageStore.openCalls != 2 || h.store.withinTxCalls != 2 {
		t.Fatalf("latest work = aggregate:%d legacy:%d touch:%d open:%d tx:%d", h.tx.latestAggregateCalls, h.tx.latestPasteCalls, h.tx.touchPasteCalls, h.imageStore.openCalls, h.store.withinTxCalls)
	}
	for index, duringTx := range h.imageStore.openDuringTx {
		if duringTx {
			t.Fatalf("latest image open %d ran inside transaction", index)
		}
	}
}

func identityConnectorPrincipal() Principal {
	return Principal{WorkspaceID: attachmentWorkspaceID, DeviceID: attachmentDeviceID, Scope: "connector"}
}

func TestListPastesAndSnapshotUseOneAggregateReadAndKeepLegacyImages(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "text plus assets")
	setAttachmentComponent(h.tx, attachmentWorkspaceID, attachmentPasteID, attachmentOldID, h.now, []ImageAsset{{
		AssetIndex: 0, MIMEType: "image/bmp", Width: 2, Height: 3, ByteSize: 28,
		ExpiresAt: h.now.Add(ImageLifetime), StorageKey: "metadata-only",
	}})
	combined := clonePasteAggregate(h.tx.aggregates[aggregateMapKey(attachmentWorkspaceID, attachmentPasteID)])
	legacyPasteID := "00000000-0000-4000-8000-000000000c21"
	legacyRevisionID := "00000000-0000-4000-8000-000000000c22"
	legacyExpiry := h.now.Add(ImageLifetime)
	legacy := PasteAggregate{
		PasteID: legacyPasteID, RevisionID: legacyRevisionID,
		AttachmentRevisionID: legacyRevisionID, ServerSequence: 2,
		CreatedAt: h.now.Add(-time.Hour), AttachmentExpiresAt: legacyExpiry,
		AttachmentRevision: &TextRevision{
			WorkspaceID: attachmentWorkspaceID, PasteID: legacyPasteID,
			RevisionID: legacyRevisionID, RevisionKind: RevisionImageBundle,
			ServerSequence: 2, CreatedAt: h.now.Add(-time.Hour), ExpiresAt: legacyExpiry,
			Assets: []ImageAsset{{AssetIndex: 0, MIMEType: "image/tiff", Width: 8, Height: 9, ByteSize: 4, ExpiresAt: legacyExpiry}},
		},
	}
	h.tx.listResult = []PasteAggregate{combined, legacy}

	listed, err := h.service.ListPastes(context.Background(), attachmentPrincipal("full"))
	if err != nil {
		t.Fatalf("ListPastes() error = %v", err)
	}
	if len(listed) != 2 || listed[0].Text == nil || *listed[0].Text != "text plus assets" || len(listed[0].Assets) != 1 || listed[1].Text != nil || listed[1].Kind != RevisionImageBundle || len(listed[1].Assets) != 1 {
		t.Fatalf("ListPastes() = %#v", listed)
	}
	if h.tx.listAggregateCalls != 1 || h.tx.legacyListCalls != 0 || !h.tx.lastListCutoff.Equal(h.now.Add(-TextHistoryWindow)) || !h.tx.lastListNow.Equal(h.now) {
		t.Fatalf("ListPastes() store calls/cutoff = aggregate:%d legacy:%d cutoff:%s now:%s", h.tx.listAggregateCalls, h.tx.legacyListCalls, h.tx.lastListCutoff, h.tx.lastListNow)
	}

	corrupt := clonePasteAggregate(combined)
	corrupt.PasteID = "00000000-0000-4000-8000-000000000c23"
	corrupt.RevisionID = "00000000-0000-4000-8000-000000000c24"
	corrupt.TextRevision.PasteID = corrupt.PasteID
	corrupt.TextRevision.RevisionID = corrupt.RevisionID
	h.tx.snapshotCursor = 42
	h.tx.snapshotResult = []PasteAggregate{combined, corrupt, legacy}
	snapshot, err := h.service.Snapshot(context.Background(), attachmentPrincipal("full"))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Cursor != 42 || len(snapshot.Pastes) != 2 || snapshot.Pastes[0].Text == nil || len(snapshot.Pastes[0].Assets) != 1 || snapshot.Pastes[1].Kind != RevisionImageBundle {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	if h.tx.snapshotCalls != 1 || h.tx.legacySnapshotCalls != 0 {
		t.Fatalf("Snapshot() store calls = aggregate:%d legacy:%d", h.tx.snapshotCalls, h.tx.legacySnapshotCalls)
	}
}

func TestReplaceAttachmentsRequiresImageStoreAndKeyring(t *testing.T) {
	h := newAttachmentHarness(t)
	h.seedText(t, "requirements")
	input := ReplaceAttachmentsInput{Assets: []images.AssetInput{attachmentBMP(0)}}
	h.service.SetImageStore(nil)
	_, err := h.service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000c31", input,
	)
	if !errors.Is(err, ErrUnavailableContent) || h.store.withinTxCalls != 0 {
		t.Fatalf("ReplaceAttachments() without image store = %v, tx=%d", err, h.store.withinTxCalls)
	}

	service := NewService(h.store, nil, h.random, attachmentClock{now: h.now})
	service.SetImageStore(h.imageStore)
	_, err = service.ReplaceAttachments(
		context.Background(), attachmentPrincipal("full"), attachmentPasteID,
		"00000000-0000-4000-8000-000000000c32", input,
	)
	if !errors.Is(err, ErrUnavailableContent) || h.store.withinTxCalls != 0 {
		t.Fatalf("ReplaceAttachments() without keyring = %v, tx=%d", err, h.store.withinTxCalls)
	}
}
