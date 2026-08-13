import Foundation
import CSQLite

public struct CachedPaste: Equatable {
    public let id: String
    public let revisionID: String
    public let sequence: Int64
    public let text: String?
    public let deleted: Bool
    public let expiresAt: Date
    public init(id: String, revisionID: String, sequence: Int64, text: String?, deleted: Bool, expiresAt: Date) {
        self.id = id; self.revisionID = revisionID; self.sequence = sequence; self.text = text; self.deleted = deleted; self.expiresAt = expiresAt
    }
}

public final class SQLiteCache {
    private var db: OpaquePointer?
    private let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    public init(path: String) throws {
        guard sqlite3_open_v2(path, &db, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX, nil) == SQLITE_OK else { throw SQLiteError.open }
        do { try execute("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;"); try execute("CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS paste_revisions (paste_id TEXT PRIMARY KEY, revision_id TEXT NOT NULL, sequence INTEGER NOT NULL, text TEXT, deleted INTEGER NOT NULL, expires_at REAL NOT NULL); CREATE TABLE IF NOT EXISTS offline_mutations (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, paste_id TEXT NOT NULL, body BLOB NOT NULL, idempotency_key TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, created_at REAL NOT NULL);") } catch { sqlite3_close(db); db = nil; throw error }
    }

    deinit { sqlite3_close(db) }

    public func savePaste(_ paste: CachedPaste) throws {
        try withStatement("INSERT INTO paste_revisions(paste_id,revision_id,sequence,text,deleted,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(paste_id) DO UPDATE SET revision_id=excluded.revision_id,sequence=excluded.sequence,text=excluded.text,deleted=excluded.deleted,expires_at=excluded.expires_at WHERE excluded.sequence >= paste_revisions.sequence;") { statement in
            bind(paste.id, at: 1, in: statement); bind(paste.revisionID, at: 2, in: statement); sqlite3_bind_int64(statement, 3, paste.sequence)
            if let text = paste.text { bind(text, at: 4, in: statement) } else { sqlite3_bind_null(statement, 4) }
            sqlite3_bind_int(statement, 5, paste.deleted ? 1 : 0); sqlite3_bind_double(statement, 6, paste.expiresAt.timeIntervalSince1970); try step(statement)
        }
    }

    public func paste(id: String) throws -> CachedPaste? {
        var result: CachedPaste?
        try withStatement("SELECT revision_id,sequence,text,deleted,expires_at FROM paste_revisions WHERE paste_id=?;") { statement in
            bind(id, at: 1, in: statement)
            if sqlite3_step(statement) == SQLITE_ROW {
                result = CachedPaste(id: id, revisionID: string(statement, 0), sequence: sqlite3_column_int64(statement, 1), text: optionalString(statement, 2), deleted: sqlite3_column_int(statement, 3) != 0, expiresAt: Date(timeIntervalSince1970: sqlite3_column_double(statement, 4)))
            }
        }
        return result
    }

    public func setCursor(_ cursor: Int64) throws { try setMetadata("cursor", value: String(cursor)) }
    public func cursor() throws -> Int64 { Int64(try metadata("cursor") ?? "0") ?? 0 }

    func enqueue(kind: MutationKind, pasteID: String, body: Data) throws -> PendingMutation {
        let key = UUID().uuidString
        try withStatement("INSERT INTO offline_mutations(kind,paste_id,body,idempotency_key,created_at) VALUES(?,?,?,?,?);") { statement in
            bind(kind.rawValue, at: 1, in: statement); bind(pasteID, at: 2, in: statement); _ = body.withUnsafeBytes { sqlite3_bind_blob(statement, 3, $0.baseAddress, Int32(body.count), transient) }; bind(key, at: 4, in: statement); sqlite3_bind_double(statement, 5, Date().timeIntervalSince1970); try step(statement)
        }
        return PendingMutation(id: Int(sqlite3_last_insert_rowid(db)), kind: kind, pasteID: pasteID, body: body, idempotencyKey: key, attempts: 0)
    }

    func pending() throws -> [PendingMutation] {
        var result: [PendingMutation] = []
        try withStatement("SELECT id,kind,paste_id,body,idempotency_key,attempts FROM offline_mutations ORDER BY id;") { statement in
            while sqlite3_step(statement) == SQLITE_ROW {
                guard let kind = MutationKind(rawValue: string(statement, 1)), let data = blob(statement, 3) else { continue }
                result.append(PendingMutation(id: Int(sqlite3_column_int64(statement, 0)), kind: kind, pasteID: string(statement, 2), body: data, idempotencyKey: string(statement, 4), attempts: Int(sqlite3_column_int(statement, 5))))
            }
        }
        return result
    }

    func retry(_ id: Int) throws -> PendingMutation {
        try withStatement("UPDATE offline_mutations SET attempts=attempts+1 WHERE id=?;") { statement in sqlite3_bind_int64(statement, 1, Int64(id)); try step(statement) }
        guard let item = try pending().first(where: { $0.id == id }) else { throw SQLiteError.query }
        return item
    }

    private func setMetadata(_ key: String, value: String) throws { try withStatement("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value;") { statement in bind(key, at: 1, in: statement); bind(value, at: 2, in: statement); try step(statement) } }
    private func metadata(_ key: String) throws -> String? { var value: String?; try withStatement("SELECT value FROM metadata WHERE key=?;") { statement in bind(key, at: 1, in: statement); if sqlite3_step(statement) == SQLITE_ROW { value = string(statement, 0) } }; return value }
    private func execute(_ sql: String) throws { guard sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK else { throw SQLiteError.query } }
    private func withStatement(_ sql: String, _ action: (OpaquePointer) throws -> Void) throws { var statement: OpaquePointer?; guard sqlite3_prepare_v2(db, sql, -1, &statement, nil) == SQLITE_OK, let statement else { throw SQLiteError.query }; defer { sqlite3_finalize(statement) }; try action(statement) }
    private func step(_ statement: OpaquePointer) throws { guard sqlite3_step(statement) == SQLITE_DONE else { throw SQLiteError.query } }
    private func bind(_ value: String, at index: Int32, in statement: OpaquePointer) { sqlite3_bind_text(statement, index, value, -1, transient) }
    private func string(_ statement: OpaquePointer, _ index: Int32) -> String { String(cString: sqlite3_column_text(statement, index)) }
    private func optionalString(_ statement: OpaquePointer, _ index: Int32) -> String? { guard let value = sqlite3_column_text(statement, index) else { return nil }; return String(cString: value) }
    private func blob(_ statement: OpaquePointer, _ index: Int32) -> Data? { guard let value = sqlite3_column_blob(statement, index) else { return nil }; return Data(bytes: value, count: Int(sqlite3_column_bytes(statement, index))) }
}

public enum SQLiteError: Error { case open, query }

public enum MutationKind: String { case create, update, delete }
public struct PendingMutation: Equatable { public let id: Int; public let kind: MutationKind; public let pasteID: String; public let body: Data; public let idempotencyKey: String; public let attempts: Int }
