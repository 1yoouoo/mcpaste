import Foundation
import CSQLite

public struct CachedPaste: Equatable {
    public let id: String
    public let revisionID: String
    public let sequence: Int64
    public let text: String?
    public let deleted: Bool
    public let expiresAt: Date
    public let attachmentRevisionID: String?
    public let attachments: [PasteAttachment]
    public init(
        id: String,
        revisionID: String,
        sequence: Int64,
        text: String?,
        deleted: Bool,
        expiresAt: Date,
        attachmentRevisionID: String? = nil,
        attachments: [PasteAttachment] = []
    ) {
        self.id = id; self.revisionID = revisionID; self.sequence = sequence; self.text = text
        self.deleted = deleted; self.expiresAt = expiresAt
        self.attachmentRevisionID = attachmentRevisionID; self.attachments = attachments
    }
}

private struct CachedPasteState {
    let id: String
    let revisionID: String
    let sequence: Int64
    let text: String?
    let deleted: Bool
    let expiresAt: Date
    let textSequence: Int64
    let attachmentSequence: Int64
    let attachmentRevisionID: String?
}

public final class SQLiteCache {
    private var db: OpaquePointer?
    private let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    public init(path: String) throws {
        guard sqlite3_open_v2(path, &db, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX, nil) == SQLITE_OK else { throw SQLiteError.open }
        do {
            try execute("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;")
            try execute("CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);")
            try execute("""
                CREATE TABLE IF NOT EXISTS paste_revisions (
                    paste_id TEXT PRIMARY KEY,
                    revision_id TEXT NOT NULL,
                    sequence INTEGER NOT NULL,
                    text TEXT,
                    deleted INTEGER NOT NULL,
                    expires_at REAL NOT NULL,
                    text_sequence INTEGER NOT NULL DEFAULT 0,
                    attachment_sequence INTEGER NOT NULL DEFAULT 0,
                    attachment_revision_id TEXT
                );
                """)
            try ensurePasteRevisionColumns()
            try execute("""
                CREATE TABLE IF NOT EXISTS cached_attachments (
                    paste_id TEXT NOT NULL,
                    asset_index INTEGER NOT NULL,
                    mime_type TEXT NOT NULL,
                    width INTEGER NOT NULL,
                    height INTEGER NOT NULL,
                    byte_size INTEGER NOT NULL,
                    expires_at REAL NOT NULL,
                    PRIMARY KEY (paste_id, asset_index),
                    FOREIGN KEY (paste_id) REFERENCES paste_revisions(paste_id) ON DELETE CASCADE ON UPDATE CASCADE
                );
                CREATE TABLE IF NOT EXISTS offline_mutations (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    kind TEXT NOT NULL,
                    paste_id TEXT NOT NULL,
                    body BLOB NOT NULL,
                    idempotency_key TEXT NOT NULL,
                    attempts INTEGER NOT NULL DEFAULT 0,
                    created_at REAL NOT NULL
                );
                """)
        } catch {
            sqlite3_close(db); db = nil; throw error
        }
    }

    deinit { sqlite3_close(db) }

    public func savePaste(_ paste: CachedPaste) throws {
        try execute("BEGIN IMMEDIATE;")
        do {
            try savePasteInTransaction(paste)
            try execute("COMMIT;")
        } catch {
            _ = try? execute("ROLLBACK;")
            throw error
        }
    }

    public func replacePastes(_ pastes: [CachedPaste]) throws {
        try execute("BEGIN IMMEDIATE;")
        do {
            try execute("DELETE FROM paste_revisions;")
            for paste in pastes { try savePasteInTransaction(paste) }
            try execute("COMMIT;")
        } catch {
            _ = try? execute("ROLLBACK;")
            throw error
        }
    }

    public func paste(id: String) throws -> CachedPaste? {
        guard let state = try state(for: id) else { return nil }
        return try makeCachedPaste(from: state)
    }

    public func apply(_ event: SyncEvent) throws {
        try execute("BEGIN IMMEDIATE;")
        do {
            switch event.kind {
            case .content:
                try applyContent(event)
            case .attachmentBundle, .imageBundle:
                try applyAttachments(event)
            case .tombstone:
                try applyTombstone(event)
            }
            try execute("COMMIT;")
        } catch {
            _ = try? execute("ROLLBACK;")
            throw error
        }
    }

    public func setCursor(_ cursor: Int64) throws { try setMetadata("cursor", value: String(cursor)) }
    public func cursor() throws -> Int64 { Int64(try metadata("cursor") ?? "0") ?? 0 }

    public func allPastes() throws -> [CachedPaste] {
        var ids: [String] = []
        try withStatement("SELECT paste_id FROM paste_revisions ORDER BY sequence DESC;") { statement in
            while true {
                let code = sqlite3_step(statement)
                if code == SQLITE_ROW {
                    ids.append(string(statement, 0))
                } else if code == SQLITE_DONE {
                    break
                } else {
                    throw SQLiteError.query
                }
            }
        }
        var result: [CachedPaste] = []
        for id in ids {
            if let paste = try paste(id: id) { result.append(paste) }
        }
        return result
    }

	func enqueue(kind: MutationKind, pasteID: String, body: Data, idempotencyKey: String? = nil) throws -> PendingMutation {
		let key = idempotencyKey ?? UUID().uuidString.lowercased()
        try withStatement("INSERT INTO offline_mutations(kind,paste_id,body,idempotency_key,created_at) VALUES(?,?,?,?,?);") { statement in
            bind(kind.rawValue, at: 1, in: statement); bind(pasteID, at: 2, in: statement); _ = body.withUnsafeBytes { sqlite3_bind_blob(statement, 3, $0.baseAddress, Int32(body.count), transient) }; bind(key, at: 4, in: statement); sqlite3_bind_double(statement, 5, Date().timeIntervalSince1970); try step(statement)
        }
        return PendingMutation(id: Int(sqlite3_last_insert_rowid(db)), kind: kind, pasteID: pasteID, body: body, idempotencyKey: key, attempts: 0)
    }

    func pending() throws -> [PendingMutation] {
        var result: [PendingMutation] = []
        try withStatement("SELECT id,kind,paste_id,body,idempotency_key,attempts FROM offline_mutations ORDER BY id;") { statement in
            while true {
                let code = sqlite3_step(statement)
                if code == SQLITE_ROW {
                    guard let kind = MutationKind(rawValue: string(statement, 1)), let data = blob(statement, 3) else { continue }
                    result.append(PendingMutation(id: Int(sqlite3_column_int64(statement, 0)), kind: kind, pasteID: string(statement, 2), body: data, idempotencyKey: string(statement, 4), attempts: Int(sqlite3_column_int(statement, 5))))
                } else if code == SQLITE_DONE {
                    break
                } else {
                    throw SQLiteError.query
                }
            }
        }
        return result
    }

    func retry(_ id: Int) throws -> PendingMutation {
        try withStatement("UPDATE offline_mutations SET attempts=attempts+1 WHERE id=?;") { statement in sqlite3_bind_int64(statement, 1, Int64(id)); try step(statement) }
        guard let item = try pending().first(where: { $0.id == id }) else { throw SQLiteError.query }
        return item
    }

    func remove(_ id: Int) throws {
        try withStatement("DELETE FROM offline_mutations WHERE id=?;") { statement in sqlite3_bind_int64(statement, 1, Int64(id)); try step(statement) }
    }

    func remapPasteID(from localID: String, to serverID: String) throws {
        try execute("BEGIN IMMEDIATE;")
        do {
            try withStatement("UPDATE paste_revisions SET paste_id=? WHERE paste_id=?;") { statement in
                bind(serverID, at: 1, in: statement); bind(localID, at: 2, in: statement); try step(statement)
            }
            try withStatement("UPDATE offline_mutations SET paste_id=? WHERE paste_id=?;") { statement in
                bind(serverID, at: 1, in: statement); bind(localID, at: 2, in: statement); try step(statement)
            }
            try execute("COMMIT;")
        } catch {
            _ = try? execute("ROLLBACK;")
            throw error
        }
    }

    func removePending(forPasteID pasteID: String) throws {
        try withStatement("DELETE FROM offline_mutations WHERE paste_id=?;") { statement in
            bind(pasteID, at: 1, in: statement); try step(statement)
        }
    }

    private func ensurePasteRevisionColumns() throws {
        let columns = try tableColumns(for: "paste_revisions")
        var didAddColumn = false
        if !columns.contains("text_sequence") {
            try execute("ALTER TABLE paste_revisions ADD COLUMN text_sequence INTEGER NOT NULL DEFAULT 0;")
            didAddColumn = true
        }
        if !columns.contains("attachment_sequence") {
            try execute("ALTER TABLE paste_revisions ADD COLUMN attachment_sequence INTEGER NOT NULL DEFAULT 0;")
            didAddColumn = true
        }
        if !columns.contains("attachment_revision_id") {
            try execute("ALTER TABLE paste_revisions ADD COLUMN attachment_revision_id TEXT;")
            didAddColumn = true
        }
        guard didAddColumn else { return }
        try execute("""
            UPDATE paste_revisions
            SET text_sequence = CASE
                    WHEN deleted = 1 THEN sequence
                    WHEN text IS NOT NULL AND text_sequence < sequence THEN sequence
                    ELSE text_sequence
                END,
                attachment_sequence = CASE
                    WHEN deleted = 1 THEN sequence
                    ELSE attachment_sequence
                END;
            """)
    }

    private func tableColumns(for table: String) throws -> Set<String> {
        var result = Set<String>()
        try withStatement("PRAGMA table_info(\(table));") { statement in
            while true {
                let code = sqlite3_step(statement)
                if code == SQLITE_ROW {
                    result.insert(string(statement, 1))
                } else if code == SQLITE_DONE {
                    break
                } else {
                    throw SQLiteError.query
                }
            }
        }
        return result
    }

    private func savePasteInTransaction(_ paste: CachedPaste) throws {
        if let existing = try state(for: paste.id), paste.sequence < existing.sequence { return }
        let textSequence: Int64 = paste.deleted || paste.text != nil ? paste.sequence : 0
        let attachmentSequence: Int64 = paste.deleted || paste.attachmentRevisionID != nil || !paste.attachments.isEmpty ? paste.sequence : 0
        try withStatement("""
            INSERT INTO paste_revisions(
                paste_id, revision_id, sequence, text, deleted, expires_at,
                text_sequence, attachment_sequence, attachment_revision_id
            ) VALUES(?,?,?,?,?,?,?,?,?)
            ON CONFLICT(paste_id) DO UPDATE SET
                revision_id=excluded.revision_id,
                sequence=excluded.sequence,
                text=excluded.text,
                deleted=excluded.deleted,
                expires_at=excluded.expires_at,
                text_sequence=excluded.text_sequence,
                attachment_sequence=excluded.attachment_sequence,
                attachment_revision_id=excluded.attachment_revision_id
            WHERE excluded.sequence >= paste_revisions.sequence;
            """) { statement in
            bind(paste.id, at: 1, in: statement)
            bind(paste.revisionID, at: 2, in: statement)
            sqlite3_bind_int64(statement, 3, paste.sequence)
            bindOptional(paste.text, at: 4, in: statement)
            sqlite3_bind_int(statement, 5, paste.deleted ? 1 : 0)
            sqlite3_bind_double(statement, 6, paste.expiresAt.timeIntervalSince1970)
            sqlite3_bind_int64(statement, 7, textSequence)
            sqlite3_bind_int64(statement, 8, attachmentSequence)
            bindOptional(paste.deleted ? nil : paste.attachmentRevisionID, at: 9, in: statement)
            try step(statement)
        }
        try replaceAttachments(pasteID: paste.id, attachments: paste.attachments)
    }

    private func applyContent(_ event: SyncEvent) throws {
        let current = try state(for: event.pasteID)
        if let current, event.sequence <= current.textSequence { return }
        let textSequence = event.sequence
        let attachmentSequence = current?.attachmentSequence ?? 0
        let aggregateUsesText = textSequence >= attachmentSequence
        if current == nil {
            try insertShell(
                pasteID: event.pasteID,
                revisionID: event.revisionID,
                sequence: event.sequence,
                text: event.text,
                deleted: event.deleted,
                textSequence: textSequence,
                attachmentSequence: 0,
                attachmentRevisionID: nil
            )
            return
        }
        try updateAggregate(
            pasteID: event.pasteID,
            revisionID: aggregateUsesText ? event.revisionID : current!.revisionID,
            sequence: aggregateUsesText ? event.sequence : current!.sequence,
            text: event.text,
            deleted: event.deleted,
            textSequence: textSequence,
            attachmentSequence: attachmentSequence,
            attachmentRevisionID: current!.attachmentRevisionID
        )
    }

    private func applyAttachments(_ event: SyncEvent) throws {
        guard let attachments = event.attachments else { throw SQLiteError.query }
        let current = try state(for: event.pasteID)
        if let current, event.sequence <= current.attachmentSequence { return }
        let textSequence = current?.textSequence ?? 0
        let aggregateUsesAttachments = event.sequence >= textSequence
        if current == nil {
            try insertShell(
                pasteID: event.pasteID,
                revisionID: event.revisionID,
                sequence: event.sequence,
                text: nil,
                deleted: false,
                textSequence: 0,
                attachmentSequence: event.sequence,
                attachmentRevisionID: event.revisionID
            )
        } else {
            try updateAggregate(
                pasteID: event.pasteID,
                revisionID: aggregateUsesAttachments ? event.revisionID : current!.revisionID,
                sequence: aggregateUsesAttachments ? event.sequence : current!.sequence,
                text: current!.text,
                deleted: false,
                textSequence: textSequence,
                attachmentSequence: event.sequence,
                attachmentRevisionID: event.revisionID
            )
        }
        try replaceAttachments(pasteID: event.pasteID, attachments: attachments)
    }

    private func applyTombstone(_ event: SyncEvent) throws {
        if let current = try state(for: event.pasteID), event.sequence <= current.sequence { return }
        if try state(for: event.pasteID) == nil {
            try insertShell(
                pasteID: event.pasteID,
                revisionID: event.revisionID,
                sequence: event.sequence,
                text: nil,
                deleted: true,
                textSequence: event.sequence,
                attachmentSequence: event.sequence,
                attachmentRevisionID: nil
            )
        } else {
            try updateAggregate(
                pasteID: event.pasteID,
                revisionID: event.revisionID,
                sequence: event.sequence,
                text: nil,
                deleted: true,
                textSequence: event.sequence,
                attachmentSequence: event.sequence,
                attachmentRevisionID: nil
            )
        }
        try replaceAttachments(pasteID: event.pasteID, attachments: [])
    }

    private func insertShell(
        pasteID: String,
        revisionID: String,
        sequence: Int64,
        text: String?,
        deleted: Bool,
        textSequence: Int64,
        attachmentSequence: Int64,
        attachmentRevisionID: String?
    ) throws {
        try withStatement("""
            INSERT INTO paste_revisions(
                paste_id, revision_id, sequence, text, deleted, expires_at,
                text_sequence, attachment_sequence, attachment_revision_id
            ) VALUES(?,?,?,?,?,?,?,?,?);
            """) { statement in
            bind(pasteID, at: 1, in: statement)
            bind(revisionID, at: 2, in: statement)
            sqlite3_bind_int64(statement, 3, sequence)
            bindOptional(text, at: 4, in: statement)
            sqlite3_bind_int(statement, 5, deleted ? 1 : 0)
            sqlite3_bind_double(statement, 6, Date.distantFuture.timeIntervalSince1970)
            sqlite3_bind_int64(statement, 7, textSequence)
            sqlite3_bind_int64(statement, 8, attachmentSequence)
            bindOptional(attachmentRevisionID, at: 9, in: statement)
            try step(statement)
        }
    }

    private func updateAggregate(
        pasteID: String,
        revisionID: String,
        sequence: Int64,
        text: String?,
        deleted: Bool,
        textSequence: Int64,
        attachmentSequence: Int64,
        attachmentRevisionID: String?
    ) throws {
        try withStatement("""
            UPDATE paste_revisions SET
                revision_id=?, sequence=?, text=?, deleted=?,
                text_sequence=?, attachment_sequence=?, attachment_revision_id=?
            WHERE paste_id=?;
            """) { statement in
            bind(revisionID, at: 1, in: statement)
            sqlite3_bind_int64(statement, 2, sequence)
            bindOptional(text, at: 3, in: statement)
            sqlite3_bind_int(statement, 4, deleted ? 1 : 0)
            sqlite3_bind_int64(statement, 5, textSequence)
            sqlite3_bind_int64(statement, 6, attachmentSequence)
            bindOptional(attachmentRevisionID, at: 7, in: statement)
            bind(pasteID, at: 8, in: statement)
            try step(statement)
        }
    }

    private func replaceAttachments(pasteID: String, attachments: [PasteAttachment]) throws {
        try withStatement("DELETE FROM cached_attachments WHERE paste_id=?;") { statement in
            bind(pasteID, at: 1, in: statement); try step(statement)
        }
        for attachment in attachments {
            try withStatement("""
                INSERT INTO cached_attachments(
                    paste_id, asset_index, mime_type, width, height, byte_size, expires_at
                ) VALUES(?,?,?,?,?,?,?);
                """) { statement in
                bind(pasteID, at: 1, in: statement)
                sqlite3_bind_int(statement, 2, Int32(attachment.assetIndex))
                bind(attachment.mimeType, at: 3, in: statement)
                sqlite3_bind_int(statement, 4, Int32(attachment.width))
                sqlite3_bind_int(statement, 5, Int32(attachment.height))
                sqlite3_bind_int64(statement, 6, attachment.byteSize)
                sqlite3_bind_double(statement, 7, attachment.expiresAt.timeIntervalSince1970)
                try step(statement)
            }
        }
    }

    private func state(for id: String) throws -> CachedPasteState? {
        var result: CachedPasteState?
        try withStatement("""
            SELECT revision_id, sequence, text, deleted, expires_at,
                   text_sequence, attachment_sequence, attachment_revision_id
            FROM paste_revisions WHERE paste_id=?;
        """) { statement in
            bind(id, at: 1, in: statement)
            let code = sqlite3_step(statement)
            if code == SQLITE_ROW {
                result = CachedPasteState(
                    id: id,
                    revisionID: string(statement, 0),
                    sequence: sqlite3_column_int64(statement, 1),
                    text: optionalString(statement, 2),
                    deleted: sqlite3_column_int(statement, 3) != 0,
                    expiresAt: Date(timeIntervalSince1970: sqlite3_column_double(statement, 4)),
                    textSequence: sqlite3_column_int64(statement, 5),
                    attachmentSequence: sqlite3_column_int64(statement, 6),
                    attachmentRevisionID: optionalString(statement, 7)
                )
            } else if code != SQLITE_DONE {
                throw SQLiteError.query
            }
        }
        return result
    }

    private func makeCachedPaste(from state: CachedPasteState) throws -> CachedPaste {
        var attachments: [PasteAttachment] = []
        try withStatement("""
            SELECT asset_index, mime_type, width, height, byte_size, expires_at
            FROM cached_attachments WHERE paste_id=? ORDER BY asset_index ASC;
        """) { statement in
            bind(state.id, at: 1, in: statement)
            while true {
                let code = sqlite3_step(statement)
                if code == SQLITE_ROW {
                    attachments.append(PasteAttachment(
                        assetIndex: Int(sqlite3_column_int(statement, 0)),
                        mimeType: string(statement, 1),
                        width: Int(sqlite3_column_int(statement, 2)),
                        height: Int(sqlite3_column_int(statement, 3)),
                        byteSize: sqlite3_column_int64(statement, 4),
                        expiresAt: Date(timeIntervalSince1970: sqlite3_column_double(statement, 5))
                    ))
                } else if code == SQLITE_DONE {
                    break
                } else {
                    throw SQLiteError.query
                }
            }
        }
        return CachedPaste(
            id: state.id,
            revisionID: state.revisionID,
            sequence: state.sequence,
            text: state.text,
            deleted: state.deleted,
            expiresAt: state.expiresAt,
            attachmentRevisionID: state.attachmentRevisionID,
            attachments: attachments
        )
    }

    private func setMetadata(_ key: String, value: String) throws { try withStatement("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value;") { statement in bind(key, at: 1, in: statement); bind(value, at: 2, in: statement); try step(statement) } }
    private func metadata(_ key: String) throws -> String? {
        var value: String?
        try withStatement("SELECT value FROM metadata WHERE key=?;") { statement in
            bind(key, at: 1, in: statement)
            let code = sqlite3_step(statement)
            if code == SQLITE_ROW {
                value = string(statement, 0)
            } else if code != SQLITE_DONE {
                throw SQLiteError.query
            }
        }
        return value
    }
    private func execute(_ sql: String) throws { guard sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK else { throw SQLiteError.query } }
    private func withStatement(_ sql: String, _ action: (OpaquePointer) throws -> Void) throws { var statement: OpaquePointer?; guard sqlite3_prepare_v2(db, sql, -1, &statement, nil) == SQLITE_OK, let statement else { throw SQLiteError.query }; defer { sqlite3_finalize(statement) }; try action(statement) }
    private func step(_ statement: OpaquePointer) throws { guard sqlite3_step(statement) == SQLITE_DONE else { throw SQLiteError.query } }
    private func bind(_ value: String, at index: Int32, in statement: OpaquePointer) { sqlite3_bind_text(statement, index, value, -1, transient) }
    private func bindOptional(_ value: String?, at index: Int32, in statement: OpaquePointer) { if let value { bind(value, at: index, in: statement) } else { sqlite3_bind_null(statement, index) } }
    private func string(_ statement: OpaquePointer, _ index: Int32) -> String { String(cString: sqlite3_column_text(statement, index)) }
    private func optionalString(_ statement: OpaquePointer, _ index: Int32) -> String? { guard let value = sqlite3_column_text(statement, index) else { return nil }; return String(cString: value) }
    private func blob(_ statement: OpaquePointer, _ index: Int32) -> Data? {
        guard sqlite3_column_type(statement, index) != SQLITE_NULL else { return nil }
        let count = Int(sqlite3_column_bytes(statement, index))
        guard count > 0 else { return Data() }
        guard let value = sqlite3_column_blob(statement, index) else { return nil }
        return Data(bytes: value, count: count)
    }
}

public enum SQLiteError: Error { case open, query }

public enum MutationKind: String { case create, update, delete, replaceAttachments = "replace_attachments" }
public struct PendingMutation: Equatable { public let id: Int; public let kind: MutationKind; public let pasteID: String; public let body: Data; public let idempotencyKey: String; public let attempts: Int }
