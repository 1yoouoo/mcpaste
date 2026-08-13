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
    public init() {}

    public func normalize(_ data: Data) throws -> NormalizedImage {
        guard data.count <= Self.maxSourceBytes else { throw ImageNormalizationError.tooLarge }
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { throw ImageNormalizationError.malformed }
        guard CGImageSourceGetCount(source) == 1 else { throw ImageNormalizationError.animated }
        guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: 100_000
        ] as CFDictionary) else { throw ImageNormalizationError.malformed }
        let output = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(output, UTType.png.identifier as CFString, 1, nil) else { throw ImageNormalizationError.encodingFailed }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else { throw ImageNormalizationError.encodingFailed }
        return NormalizedImage(mimeType: "image/png", width: image.width, height: image.height, data: output as Data)
    }
}
