import AppKit
import Foundation
import XCTest
@testable import SwarmBarKit

/// Attachment behaviour ported from NewOrchestratorFormTests (the images
/// section), now against the shared model both windows use. The form-level
/// body/submit tests stay on NewOrchestratorFormTests.
@MainActor
final class RequestImagesModelTests: XCTestCase {
    /// A valid 1×1 PNG, inline so the images tests need no fixture file.
    private var pngData: Data {
        Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
              0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
              0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0xF8, 0x0F, 0x00, 0x00,
              0x01, 0x01, 0x00, 0x05, 0x18, 0xD8, 0x4E, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
              0x42, 0x60, 0x82])
    }

    func testAddImageKeepsPNGAsIs() {
        let m = RequestImagesModel()
        m.addImage(data: pngData, name: "a.png")
        XCTAssertEqual(m.images.count, 1)
        XCTAssertEqual(m.images.first?.data, pngData)
        XCTAssertNil(m.error)
    }

    func testAddImageConvertsTIFFToPNG() {
        let m = RequestImagesModel()
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        let tiff = rep.tiffRepresentation!
        m.addImage(data: tiff, name: "a.tiff")
        XCTAssertEqual(m.images.count, 1)
        XCTAssertEqual(m.images.first?.data.prefix(4), Data([0x89, 0x50, 0x4E, 0x47]))
    }

    func testAddImagePassesNonImageFileThroughUnchanged() {
        let m = RequestImagesModel()
        let pdf = Data("%PDF-1.7\n1 0 obj\n".utf8)
        let md = Data("# Notes\n".utf8)
        m.addImage(data: pdf, name: "spec.pdf")
        m.addImage(data: md, name: "notes.md")
        XCTAssertNil(m.error)
        XCTAssertEqual(m.images.map(\.name), ["spec.pdf", "notes.md"])
        XCTAssertEqual(m.images.map(\.data), [pdf, md])
        XCTAssertEqual(m.images.map(\.isImage), [false, false])
    }

    func testAddImageMarksRealImagesAsImages() {
        let m = RequestImagesModel()
        m.addImage(data: pngData, name: "a.png")
        XCTAssertEqual(m.images.first?.isImage, true)
    }

    func testAttachmentCapIsSharedAcrossImagesAndFiles() {
        let m = RequestImagesModel()
        for i in 0..<5 { m.addImage(data: pngData, name: "\(i).png") }
        for i in 0..<5 { m.addImage(data: Data("x".utf8), name: "\(i).txt") }
        XCTAssertNil(m.error)
        XCTAssertEqual(m.images.count, 10)
        m.addImage(data: Data("x".utf8), name: "eleventh.zip")
        XCTAssertEqual(m.error, "Up to 10 attachments.")
        XCTAssertEqual(m.images.count, 10)
    }

    func testAddImageRejectsOversizeFile() {
        let m = RequestImagesModel()
        m.addImage(data: Data(count: (10 << 20) + 1), name: "big.pdf")
        XCTAssertEqual(m.error, "big.pdf is larger than 10 MB.")
        XCTAssertEqual(m.images.count, 0)
    }

    func testAddImageAcceptsFileOfExactlyTenMB() {
        let m = RequestImagesModel()
        m.addImage(data: Data(count: 10 << 20), name: "max.bin")
        XCTAssertNil(m.error)
        XCTAssertEqual(m.images.count, 1)
    }

    func testAddImageCapsAtTen() {
        let m = RequestImagesModel()
        for i in 0..<11 { m.addImage(data: pngData, name: "\(i).png") }
        XCTAssertEqual(m.error, "Up to 10 attachments.")
        XCTAssertEqual(m.images.count, 10)
    }

    func testAddImageRejectsOversize() {
        let m = RequestImagesModel()
        var big = pngData
        big.append(Data(count: 10 << 20))
        m.addImage(data: big, name: "big.png")
        XCTAssertEqual(m.error, "big.png is larger than 10 MB.")
        XCTAssertEqual(m.images.count, 0)
    }

    func testRemoveImageClearsErrorAndEntry() {
        let m = RequestImagesModel()
        m.addImage(data: pngData, name: "a.png")
        m.addImage(data: Data("x".utf8), name: "big.txt")
        for i in 0..<10 { m.addImage(data: pngData, name: "\(i).png") }
        XCTAssertNotNil(m.error)
        m.removeImage(m.images[0].id)
        XCTAssertEqual(m.images.count, 9)
        XCTAssertNil(m.error)
    }

    func testPayloadIsNilWhenEmptyAndBase64WhenPresent() {
        let m = RequestImagesModel()
        XCTAssertNil(m.payload)
        let pdf = Data("%PDF-1.7".utf8)
        m.addImage(data: pdf, name: "Quarterly Report.final.pdf")
        XCTAssertEqual(m.payload, [AttachmentPayload(name: "Quarterly Report.final.pdf", data: pdf.base64EncodedString())])
    }
}
