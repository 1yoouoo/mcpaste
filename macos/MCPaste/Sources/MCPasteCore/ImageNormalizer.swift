import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

public struct NormalizedImage: Equatable {
    public let mimeType: String
    public let width: Int
    public let height: Int
    public let data: Data
    public init(mimeType: String, width: Int, height: Int, data: Data) {
        self.mimeType = mimeType; self.width = width; self.height = height; self.data = data
    }
}

public enum ImageNormalizationError: Error { case tooLarge, unsupported, animated, malformed, encodingFailed }

public final class ImageNormalizer {
    public static let maxSourceBytes = 250 * 1024 * 1024
    public static let maxNormalizedJPEGBytes = 4 * 1024 * 1024
    public static let maxNormalizedPNGBytes = 8 * 1024 * 1024
    public static let maxBundleBytes = 32 * 1024 * 1024
    public static let maxBundleItems = 20
    public static let maxAttachmentItems = 8
    public init() {}

    public static func validateAttachmentCount(_ count: Int) throws {
        guard (0...maxAttachmentItems).contains(count) else { throw ImageNormalizationError.tooLarge }
    }

    public func normalize(_ data: Data) throws -> NormalizedImage {
        guard data.count <= Self.maxSourceBytes else { throw ImageNormalizationError.tooLarge }
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { throw ImageNormalizationError.malformed }
        guard CGImageSourceGetCount(source) == 1 else { throw ImageNormalizationError.animated }
        for maxPixelSize in [2_048, 1_536, 1_024, 768] {
            guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: maxPixelSize
            ] as CFDictionary) else { throw ImageNormalizationError.malformed }
            let alpha = image.alphaInfo != .none && image.alphaInfo != .noneSkipFirst && image.alphaInfo != .noneSkipLast
            let type = alpha ? UTType.png : UTType.jpeg
            var quality = 0.82
            for _ in 0..<6 {
                let output = NSMutableData()
                guard let destination = CGImageDestinationCreateWithData(output, type.identifier as CFString, 1, nil) else { throw ImageNormalizationError.encodingFailed }
                let properties: [CFString: Any] = alpha ? [:] : [kCGImageDestinationLossyCompressionQuality: quality]
                CGImageDestinationAddImage(destination, image, properties as CFDictionary)
                guard CGImageDestinationFinalize(destination) else { throw ImageNormalizationError.encodingFailed }
                let maxBytes = alpha ? Self.maxNormalizedPNGBytes : Self.maxNormalizedJPEGBytes
                if output.length <= maxBytes {
                    return NormalizedImage(mimeType: alpha ? "image/png" : "image/jpeg", width: image.width, height: image.height, data: output as Data)
                }
                quality *= 0.8
            }
        }
        throw ImageNormalizationError.tooLarge
    }
}
