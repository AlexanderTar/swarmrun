import AppKit
import Foundation
import Observation

/// One attachment on the request, an image or any other file. `data` is what gets uploaded:
/// original bytes for a web format or a non-image file, PNG bytes for an image `addImage`
/// converted. The thumbnail is derived in the view (`NSImage(data:)`) for images only, not stored
/// here, so this stays `Equatable`.
public struct RequestImage: Identifiable, Equatable {
    public let id: UUID
    public var name: String
    public var data: Data
    /// False for a file Swarm can't read as an image; the strip draws an extension tile for it.
    public var isImage: Bool
}

/// Attachment state shared by the New orchestrator window and the Orchestrate
/// task window: up to 10 files of any type, 10 MB each.
@MainActor
@Observable
public final class RequestImagesModel {
    public private(set) var images: [RequestImage] = []
    public private(set) var error: String?

    public init() {}

    /// Reads each URL's data and adds it, using the last path component as the name.
    public func addImages(from urls: [URL]) {
        for url in urls {
            guard let data = try? Data(contentsOf: url) else { continue }
            addImage(data: data, name: url.lastPathComponent)
        }
    }

    /// PNG, JPEG, GIF and WebP bytes are kept as-is; any other image `NSImage` can read is converted
    /// to PNG; every other file passes through unchanged. An 11th attachment or one over 10 MB sets
    /// `error` instead.
    public func addImage(data: Data, name: String) {
        guard images.count < Self.maxImageCount else {
            error = Copy.imageTooMany
            return
        }
        let converted = Self.uploadable(data)
        let uploadable = converted ?? data
        guard uploadable.count <= Self.maxImageBytes else {
            error = Copy.imageTooLarge(name)
            return
        }
        images.append(RequestImage(id: UUID(), name: name, data: uploadable, isImage: converted != nil))
        error = nil
    }

    public func removeImage(_ id: UUID) {
        images.removeAll { $0.id == id }
        error = nil
    }

    /// Base64 upload bodies, nil when there is nothing to send.
    public var payload: [AttachmentPayload]? {
        images.isEmpty ? nil : images.map { AttachmentPayload(name: $0.name, data: $0.data.base64EncodedString()) }
    }

    private static let maxImageCount = 10
    private static let maxImageBytes = 10 << 20

    private static let pngMagic: [UInt8] = [0x89, 0x50, 0x4E, 0x47]
    private static let jpegMagic: [UInt8] = [0xFF, 0xD8, 0xFF]
    private static let gifMagic: [UInt8] = [0x47, 0x49, 0x46, 0x38]

    /// Web formats pass through unchanged (sniffed by magic bytes); anything else `NSImage` can
    /// read is converted to PNG. `nil` when the bytes aren't a readable image (a plain file).
    static func uploadable(_ data: Data) -> Data? {
        if data.starts(with: pngMagic) || data.starts(with: jpegMagic) || data.starts(with: gifMagic) { return data }
        if data.count >= 12, data.prefix(4).elementsEqual([0x52, 0x49, 0x46, 0x46]),
           data[data.index(data.startIndex, offsetBy: 8)..<data.index(data.startIndex, offsetBy: 12)]
               .elementsEqual([0x57, 0x45, 0x42, 0x50]) { return data }
        return NSBitmapImageRep(data: data)?.representation(using: .png, properties: [:])
    }
}
