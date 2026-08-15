import Foundation

public enum MCPasteEndpointError: Error, Equatable {
    case notConfigured
    case invalid
}

public enum MCPasteEndpoint {
    public static func baseURL() throws -> URL {
        let value = MCPasteBuildConfiguration.endpoint
        guard !value.isEmpty else { throw MCPasteEndpointError.notConfigured }
        guard let url = URL(string: value),
              url.scheme == "https",
              url.user == nil,
              let host = url.host,
              !host.isEmpty,
              url.path.isEmpty,
              url.query == nil,
              url.fragment == nil,
              !value.contains("?") && !value.contains("#") && !value.hasSuffix("/") else {
            throw MCPasteEndpointError.invalid
        }
        return url
    }

    public static func mcpURL() throws -> URL {
        try baseURL().appendingPathComponent("v1/mcp")
    }

    public static func matchesConfiguredEndpoint(_ endpoint: String?) -> Bool {
        guard let endpoint, let configured = try? baseURL() else { return false }
        return endpoint == configured.absoluteString
    }
}
