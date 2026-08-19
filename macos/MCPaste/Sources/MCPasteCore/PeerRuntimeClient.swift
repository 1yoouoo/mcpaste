import CryptoKit
import Foundation

public final class PeerRuntimeClient: PeerRuntimeServing, @unchecked Sendable {
    private static let endpoint = "http://127.0.0.1:38421"
    private static let maxTextBytes = 4 * 1024 * 1024
    // A max-size UTF-8 context may be represented entirely by six-byte JSON
    // Unicode escapes; the remainder is bounded context metadata.
    private static let maxContextJSONBytes = maxTextBytes * 6 + 64 * 1024
    private static let maxManifestBytes = maxContextJSONBytes
    private static let maxDevicesBytes = 1024 * 1024
    private static let maxAssetBytes = 8 * 1024 * 1024
    private static let maxBundleBytes = 32 * 1024 * 1024
    private static let maxAssets = 8

    private let baseURL: URL
    private let token: String
    private let session: URLSession

    public init(baseURL: URL, token: String, session: URLSession) throws {
        guard
            baseURL.absoluteString == Self.endpoint,
            !token.isEmpty,
            token.lengthOfBytes(using: .utf8) <= 4 * 1024,
            token.unicodeScalars.allSatisfy({
                !$0.properties.isWhitespace && !CharacterSet.controlCharacters.contains($0)
            }),
            let proxy = session.configuration.connectionProxyDictionary,
            proxy.isEmpty
        else {
            throw PeerRuntimeError.invalidResponse
        }
        self.baseURL = baseURL
        self.token = token
        self.session = session
    }

    public convenience init(baseURL: URL, token: String) throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = [:]
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        try self.init(baseURL: baseURL, token: token, session: URLSession(configuration: configuration))
    }

    public func publish(
        text: String,
        images: [NormalizedImage],
        expectedRevision: RuntimeRevision?
    ) async throws -> PublicationResult {
        guard images.count <= Self.maxAssets else { throw PeerRuntimeError.rejectedImage }
        let textBytes = text.lengthOfBytes(using: .utf8)
        guard textBytes <= Self.maxTextBytes else { throw PeerRuntimeError.invalidResponse }

        var digests: [String] = []
        var bundleBytes = textBytes
        for image in images {
            guard
                (image.mimeType == "image/png" || image.mimeType == "image/jpeg"),
                image.width > 0,
                image.height > 0,
                !image.data.isEmpty,
                image.data.count <= Self.maxAssetBytes,
                bundleBytes <= Self.maxBundleBytes - image.data.count
            else {
                throw PeerRuntimeError.rejectedImage
            }
            bundleBytes += image.data.count
            let digest = Self.sha256(image.data)
            digests.append(digest)
            var request = try makeRequest(path: "/v1/local/assets/\(digest)", method: "PUT")
            request.httpBody = image.data
            request.setValue(image.mimeType, forHTTPHeaderField: "Content-Type")
            request.setValue(String(image.data.count), forHTTPHeaderField: "Content-Length")
            request.setValue(String(image.width), forHTTPHeaderField: "X-MCPaste-Width")
            request.setValue(String(image.height), forHTTPHeaderField: "X-MCPaste-Height")
            let response = try await load(request, limit: 1024)
            guard response.statusCode == 204 else { throw PeerRuntimeError.rejectedImage }
        }

        let update = LocalUpdate(text: text, assetDigests: digests, expectedRevision: expectedRevision)
        var request = try makeRequest(path: "/v1/local/context", method: "PUT")
        let body = try JSONEncoder().encode(update)
        guard body.count <= Self.maxContextJSONBytes else { throw PeerRuntimeError.invalidResponse }
        request.httpBody = body
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(String(request.httpBody?.count ?? 0), forHTTPHeaderField: "Content-Length")
        let response = try await load(request, limit: 1024)
        if response.statusCode == 409 { throw PeerRuntimeError.conflict }
        guard response.statusCode == 200 else { throw PeerRuntimeError.unavailable }
        let result: PublicationResult
        do {
            result = try Self.decoder().decode(PublicationResult.self, from: response.data)
        } catch {
            throw PeerRuntimeError.invalidResponse
        }
        guard
            !result.revision.deviceID.isEmpty,
            expectedRevision.map({ result.revision > $0 }) ?? true,
            result.syncState == .upToDate || result.syncState == .updating || result.syncState == .waitingToSync
        else {
            throw PeerRuntimeError.invalidResponse
        }
        return result
    }

    public func current() async throws -> RuntimeContext? {
        let response = try await load(try makeRequest(path: "/v1/local/context"), limit: Self.maxManifestBytes)
        if response.statusCode == 404 { throw PeerRuntimeError.empty }
        guard response.statusCode == 200 else { throw PeerRuntimeError.unavailable }

        let wire: LocalContextResponse
        do {
            wire = try Self.decoder().decode(LocalContextResponse.self, from: response.data)
        } catch {
            throw PeerRuntimeError.invalidResponse
        }
        try validate(wire)
        let assets = try await download(wire.assets)
        return RuntimeContext(
            revision: wire.revision,
            sourceDeviceID: wire.sourceDeviceID,
            updatedAt: wire.updatedAt,
            text: wire.text,
            assets: assets,
            sourceReachable: wire.sourceReachable,
            syncState: wire.syncState
        )
    }

    public func devices() async throws -> [PeerDevice] {
        let response = try await load(try makeRequest(path: "/v1/local/devices"), limit: Self.maxDevicesBytes)
        guard response.statusCode == 200 else { throw PeerRuntimeError.unavailable }
        let envelope: DevicesResponse
        do {
            envelope = try Self.decoder().decode(DevicesResponse.self, from: response.data)
        } catch {
            throw PeerRuntimeError.invalidResponse
        }
        guard envelope.devices.allSatisfy({ !$0.id.isEmpty && !$0.displayName.isEmpty }) else {
            throw PeerRuntimeError.invalidResponse
        }
        return envelope.devices
    }

    private func validate(_ response: LocalContextResponse) throws {
        guard
            response.protocolVersion == 1,
            !response.revision.deviceID.isEmpty,
            !response.sourceDeviceID.isEmpty,
            response.text.lengthOfBytes(using: .utf8) <= Self.maxTextBytes,
            response.assets.count <= Self.maxAssets
        else {
            throw PeerRuntimeError.invalidResponse
        }
        var bundleBytes = response.text.lengthOfBytes(using: .utf8)
        for asset in response.assets {
            guard
                Self.isDigest(asset.digest),
                asset.mimeType == "image/png" || asset.mimeType == "image/jpeg",
                asset.width > 0,
                asset.height > 0,
                asset.byteSize > 0,
                asset.byteSize <= Self.maxAssetBytes,
                bundleBytes <= Self.maxBundleBytes - asset.byteSize
            else {
                throw PeerRuntimeError.invalidResponse
            }
            bundleBytes += asset.byteSize
        }
    }

    private func download(_ manifests: [AssetManifest]) async throws -> [RuntimeAsset] {
        guard !manifests.isEmpty else { return [] }
        return try await withThrowingTaskGroup(of: (Int, RuntimeAsset).self) { group in
            var result = Array<RuntimeAsset?>(repeating: nil, count: manifests.count)
            var next = 0
            func addNext() {
                guard next < manifests.count else { return }
                let index = next
                let manifest = manifests[index]
                next += 1
                group.addTask { [self] in
                    (index, try await downloadAsset(manifest, index: index))
                }
            }
            for _ in 0..<min(3, manifests.count) { addNext() }
            while let (index, asset) = try await group.next() {
                result[index] = asset
                addNext()
            }
            guard result.allSatisfy({ $0 != nil }) else { throw PeerRuntimeError.invalidResponse }
            return result.compactMap { $0 }
        }
    }

    private func downloadAsset(_ manifest: AssetManifest, index: Int) async throws -> RuntimeAsset {
        let response = try await load(
            try makeRequest(path: "/v1/local/context/assets/\(index)"),
            limit: Self.maxAssetBytes
        )
        guard
            response.statusCode == 200,
            response.value(forHTTPHeaderField: "Content-Type") == manifest.mimeType,
            response.value(forHTTPHeaderField: "Content-Length") == String(manifest.byteSize),
            response.value(forHTTPHeaderField: "X-MCPaste-SHA256") == manifest.digest,
            response.data.count == manifest.byteSize,
            Self.sha256(response.data) == manifest.digest
        else {
            throw PeerRuntimeError.invalidResponse
        }
        return RuntimeAsset(
            digest: manifest.digest,
            mimeType: manifest.mimeType,
            width: manifest.width,
            height: manifest.height,
            data: response.data
        )
    }

    private func makeRequest(path: String, method: String = "GET") throws -> URLRequest {
        guard path.first == "/", !path.contains("?"), !path.contains("#") else {
            throw PeerRuntimeError.invalidResponse
        }
        let url = baseURL.appendingPathComponent(String(path.dropFirst()))
        guard url.scheme == "http", url.host == "127.0.0.1", url.port == 38421 else {
            throw PeerRuntimeError.invalidResponse
        }
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 2)
        request.httpMethod = method
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return request
    }

    private func load(_ request: URLRequest, limit: Int) async throws -> LoadedResponse {
        do {
            let delegate = NoRedirectDelegate()
            let (bytes, rawResponse) = try await session.bytes(for: request, delegate: delegate)
            guard
                let response = rawResponse as? HTTPURLResponse,
                response.url == request.url
            else {
                throw PeerRuntimeError.invalidResponse
            }
            if response.expectedContentLength > Int64(limit) {
                throw PeerRuntimeError.invalidResponse
            }
            var data = Data()
            if response.expectedContentLength > 0 {
                data.reserveCapacity(Int(response.expectedContentLength))
            }
            for try await byte in bytes {
                guard data.count < limit else { throw PeerRuntimeError.invalidResponse }
                data.append(byte)
            }
            return LoadedResponse(response: response, data: data)
        } catch let error as PeerRuntimeError {
            throw error
        } catch {
            throw PeerRuntimeError.unavailable
        }
    }

    private static func decoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer().decode(String.self)
            let fractional = ISO8601DateFormatter()
            fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = fractional.date(from: value) { return date }
            let whole = ISO8601DateFormatter()
            whole.formatOptions = [.withInternetDateTime]
            if let date = whole.date(from: value) { return date }
            throw PeerRuntimeError.invalidResponse
        }
        return decoder
    }

    private static func sha256(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    private static func isDigest(_ value: String) -> Bool {
        value.utf8.count == 64 && value.utf8.allSatisfy {
            (0x30...0x39).contains($0) || (0x61...0x66).contains($0)
        }
    }
}

private final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil)
    }
}

private struct LoadedResponse {
    let response: HTTPURLResponse
    let data: Data
    var statusCode: Int { response.statusCode }
    func value(forHTTPHeaderField name: String) -> String? { response.value(forHTTPHeaderField: name) }
}

private struct LocalUpdate: Encodable {
    let text: String
    let assetDigests: [String]
    let expectedRevision: RuntimeRevision?

    enum CodingKeys: String, CodingKey {
        case text
        case assetDigests = "asset_digests"
        case expectedRevision = "expected_revision"
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(text, forKey: .text)
        try container.encode(assetDigests, forKey: .assetDigests)
        if let expectedRevision {
            try container.encode(expectedRevision, forKey: .expectedRevision)
        } else {
            try container.encodeNil(forKey: .expectedRevision)
        }
    }
}

private struct LocalContextResponse: Decodable {
    let protocolVersion: Int
    let revision: RuntimeRevision
    let sourceDeviceID: String
    let updatedAt: Date
    let text: String
    let assets: [AssetManifest]
    let sourceReachable: Bool
    let syncState: PeerSyncState

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case revision
        case sourceDeviceID = "source_device_id"
        case updatedAt = "updated_at"
        case text
        case assets
        case sourceReachable = "source_reachable"
        case syncState = "sync_state"
    }
}

private struct AssetManifest: Decodable, Sendable {
    let digest: String
    let mimeType: String
    let width: Int
    let height: Int
    let byteSize: Int

    enum CodingKeys: String, CodingKey {
        case digest = "sha256"
        case mimeType = "mime_type"
        case width
        case height
        case byteSize = "byte_size"
    }
}

private struct DevicesResponse: Decodable {
    let devices: [PeerDevice]
}
