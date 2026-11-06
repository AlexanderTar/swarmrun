import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class BoardHandoffRenderTests: XCTestCase {
    private func render(_ form: BoardHandoffForm) -> NSHostingView<BoardHandoffView> {
        let host = NSHostingView(rootView: BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 300)
        host.layoutSubtreeIfNeeded()
        return host
    }
    private func popups(_ v: NSView) -> [NSPopUpButton] { ((v as? NSPopUpButton).map { [$0] } ?? []) + v.subviews.flatMap(popups) }

    func testItemPopupIsTwoLinesClosedAndOpen() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        let item = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertGreaterThanOrEqual(item.frame.height, 34, "closed Item popup is one two-line row tall")
        XCTAssertEqual(item.titleOfSelectedItem, "EPIC-12 · Authentication")
        let closed = try XCTUnwrap((item.cell as? NSPopUpButtonCell)?.menuItem?.attributedTitle?.string)
        XCTAssertTrue(closed.hasPrefix("EPIC-12 · Authentication\n"), "closed popup shows title then detail line")
        let rowTitle = try XCTUnwrap(item.itemArray.first?.attributedTitle?.string)
        XCTAssertTrue(rowTitle.contains("\n"), "menu rows carry a second detail line")
        XCTAssertNotNil(popups(host).first { $0.accessibilityLabel() == Copy.agent }, "shared AgentPickerGrid present")
    }

    func testEmptyStateShowsNoItemsCopy() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        client.boardItemList = []
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        XCTAssertNil(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertFalse(form.canSubmit)
    }

    func testMenuRebuildsWhenOnlyDetailLineChanges() throws {
        final class Box { var text = "Running" }
        let box = Box()
        func picker() -> WideOptionPicker {
            WideOptionPicker("Item", options: [PickerOption("a", "A")], value: "a",
                             detail: { _ in NSAttributedString(string: box.text) }) { _ in }
        }
        let host = NSHostingView(rootView: picker())
        host.frame = NSRect(x: 0, y: 0, width: 400, height: 60)
        host.layoutSubtreeIfNeeded()
        let popup = try XCTUnwrap(popups(host).first)
        XCTAssertEqual(popup.itemArray.first?.attributedTitle?.string, "A\nRunning")
        box.text = "Idle"
        host.rootView = picker()
        host.layoutSubtreeIfNeeded()
        XCTAssertEqual(popup.itemArray.first?.attributedTitle?.string, "A\nIdle")
        XCTAssertEqual((popup.cell as? NSPopUpButtonCell)?.menuItem?.attributedTitle?.string, "A\nIdle")
    }

    func testTintedIconIsNotTemplate() {
        XCTAssertFalse(Icons.tinted(IconName(.claude)).isTemplate, "attachment icons must not rely on template tinting")
    }

    func testStartModeShowsWorkerRowsPrefilledFromSettings() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        XCTAssertFalse(form.isHandoff)
        let host = render(form)
        for role in BoardHandoffForm.workerRoles {
            let accessibility = "\(Copy.defaultsRowLabel(role)) \(Copy.agent)"
            XCTAssertNotNil(popups(host).first { $0.accessibilityLabel() == accessibility }, "worker row for \(role.rawValue) in start mode")
        }
        let coder = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == "Coding Agent" }, "worker rows in start mode")
        XCTAssertEqual(coder.titleOfSelectedItem, "Claude")
    }

    func testStartModeRendersWorkerErrorAndNoteRows() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        // gpt-legacy offers only medium: the stored high effort is unavailable, leaving a note.
        form.setWorkerModel(.reviewer, "gpt-legacy")
        XCTAssertNotNil(form.workerNote(.reviewer), "reviewer note row has content")
        form.setWorkerModel(.coder, "no-such-model")
        XCTAssertNotNil(form.workerErrors(.coder).model, "coder error row has content")
        // SwiftUI Text rows are private views (no NSTextField/accessibility string in a bare
        // host), so the render half is a smoke check: the conditional error/note GridRows
        // execute without breaking the worker grid.
        let host = render(form)
        for role in BoardHandoffForm.workerRoles {
            let accessibility = "\(Copy.defaultsRowLabel(role)) \(Copy.agent)"
            XCTAssertNotNil(popups(host).first { $0.accessibilityLabel() == accessibility },
                            "worker row for \(role.rawValue) still renders with error/note rows present")
        }
    }

    func testStartModeWorkerEditAppliesWithoutSettingsDefault() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        var settings = model.state.settings
        settings[.coder] = nil
        let form = BoardHandoffForm(client: client, settings: settings, agents: model.state.agents,
                                    connected: true, preselectAgent: nil)
        await form.load()
        form.selectedKey = "BUG-7"
        form.setWorkerAgent(.coder, "codex")
        let host = render(form)
        let coder = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == "Coding Agent" })
        XCTAssertEqual(coder.titleOfSelectedItem, "Codex", "edited worker pick reaches the row picker")
    }

    func testHandoffModeShowsWorkerRowsPrefilledFromOverrides() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        var agents = model.state.agents
        let i = agents.firstIndex { $0.name == "auth-epic-orchestrator" }!
        agents[i].roleOverrides = ["coder": RoleDefault(agent: .codex, model: "gpt-6-astra")]
        let form = BoardHandoffForm(client: client, settings: model.state.settings, agents: agents,
                                    connected: true, preselectAgent: "auth-epic-orchestrator")
        await form.load()
        XCTAssertTrue(form.isHandoff)
        let host = render(form)
        let coder = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == "Coding Agent" },
                                  "hand-off mode shows the worker overrides grid")
        XCTAssertEqual(coder.titleOfSelectedItem, "Codex", "prefilled from the orchestrator's override")
    }

    /// Real NSHostingView layout: every worker row's Agent/Model/Effort popups share one frame
    /// width per column, and no popup runs into the next column (the Research row's wider
    /// Antigravity/Gemini popup used to overlap its Effort label).
    func testWorkerRowPickersShareColumnWidthsAndDoNotOverlap() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        // Real catalogs carry long labels ("Claude Sonnet 5.5 (latest)") that outgrow the Model column.
        for e in client.catalogEntries.indices {
            for m in client.catalogEntries[e].models.indices where client.catalogEntries[e].kind == .claude {
                client.catalogEntries[e].models[m].label += " (latest) with a very long suffix"
            }
        }
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        form.setWorkerAgent(.researcher, "agy")
        form.setWorkerModel(.researcher, "gemini-3.8-flash")
        form.setWorkerAgent(.mechanical, "codex")
        let host = render(form)
        host.frame = NSRect(x: 0, y: 0, width: 760, height: 300) // the window's minWidth
        host.layoutSubtreeIfNeeded()
        func frame(_ role: SettingsRole, _ kind: String) throws -> NSRect {
            let p = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == "\(Copy.defaultsRowLabel(role)) \(kind)" })
            return host.convert(p.bounds, from: p)
        }
        var agentW = Set<CGFloat>(), modelW = Set<CGFloat>(), effortW = Set<CGFloat>()
        for role in BoardHandoffForm.workerRoles {
            let a = try frame(role, Copy.agent), m = try frame(role, Copy.model)
            agentW.insert(a.width.rounded()); modelW.insert(m.width.rounded())
            XCTAssertGreaterThanOrEqual(m.minX - a.maxX, 50, "\(role) agent popup runs into the 50pt Model label")
            if form.workerEffortOptions(role) != nil {
                let e = try frame(role, Copy.effort)
                effortW.insert(e.width.rounded())
                XCTAssertGreaterThanOrEqual(e.minX - m.maxX, 45, "\(role) model popup overlaps the 45pt Effort label")
            }
        }
        XCTAssertEqual(agentW.count, 1, "agent popup widths differ across rows: \(agentW)")
        XCTAssertEqual(modelW.count, 1, "model popup widths differ across rows: \(modelW)")
        XCTAssertEqual(effortW.count, 1, "effort popup widths differ across rows: \(effortW)")
    }

    /// The orchestrator grid and the worker grid are stacked in one window: their columns must line up.
    func testWorkerGridColumnsLineUpWithOrchestratorGrid() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        let host = render(form)
        host.frame = NSRect(x: 0, y: 0, width: 760, height: 300)
        host.layoutSubtreeIfNeeded()
        func frame(_ label: String) throws -> NSRect {
            let p = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == label })
            return host.convert(p.bounds, from: p)
        }
        let orch = try frame(Copy.agent), worker = try frame("\(Copy.defaultsRowLabel(.coder)) \(Copy.agent)")
        XCTAssertEqual(orch.minX.rounded(), worker.minX.rounded(), "Agent columns are offset between the two grids")
        let orchModel = try frame(Copy.model), workerModel = try frame("\(Copy.defaultsRowLabel(.coder)) \(Copy.model)")
        XCTAssertEqual(orchModel.minX.rounded(), workerModel.minX.rounded(), "Model columns are offset between the two grids")
    }

    // MARK: loading-time validation (TASK-486)

    private func tallHost(_ form: BoardHandoffForm) -> NSHostingView<BoardHandoffView> {
        let host = NSHostingView(rootView: BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 640)
        host.layoutSubtreeIfNeeded()
        return host
    }

    func testNoValidationErrorsAtAnyLoadingState() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        client.holdCatalog = true
        let form = model.makeBoardHandoffForm()
        let host = tallHost(form)
        let before = try ocrText(host)
        XCTAssertTrue(before.contains(Copy.workerOverrides), "OCR sanity: \(before)")
        assertNoPickerErrors(before, "window shown, load not started")

        let load = Task { await form.load() }
        await waitForCall(client, "catalog")
        assertNoPickerErrors(try ocrText(host), "catalog in flight")

        client.releaseCatalog()
        await load.value
        XCTAssertFalse(form.loading)
        assertNoPickerErrors(try ocrText(host), "loaded with valid Settings")
    }

    func testGenuineValidationErrorsStillShowAfterLoad() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        var state = try client.stateResult.get()
        state.settings.enabledAgents = [.codex, .agy] // orchestrator + most worker defaults are Claude
        client.stateResult = .success(state)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        let text = try ocrText(tallHost(form))
        XCTAssertTrue(text.contains(Copy.chooseAgent), text)
        XCTAssertFalse(form.canSubmit)
    }

    func testNoDefaultsFromSettingsCaption() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        let text = try ocrText(tallHost(form))
        XCTAssertTrue(text.contains(Copy.workerOverrides), "OCR sanity: \(text)")
        XCTAssertFalse(text.contains("Defaults from Settings"), text)
    }

    // MARK: request field and files strip (unit 3)

    private func handoffForm(selectedKey: String) async throws -> BoardHandoffForm {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = selectedKey
        return form
    }

    private func contentHost(_ form: BoardHandoffForm, height: CGFloat, maxFormHeight: CGFloat? = nil,
                           onDone: @escaping () -> Void = {}) -> NSHostingView<BoardHandoffView> {
        let view = maxFormHeight.map { BoardHandoffView(form: form, onDone: onDone, onCancel: {}, maxFormHeight: $0) }
            ?? BoardHandoffView(form: form, onDone: onDone, onCancel: {})
        let host = NSHostingView(rootView: view)
        host.frame = NSRect(x: 0, y: 0, width: 820, height: height)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.15))
        return host
    }

    /// A real window around the view, like the Orchestrate task window scene:
    /// titled with a resizable-capable mask, content-driven sizing.
    private func openWindow(_ view: BoardHandoffView) -> NSWindow {
        let controller = NSHostingController(rootView: view)
        controller.sizingOptions = [.minSize, .intrinsicContentSize, .maxSize]
        let window = NSWindow(contentViewController: controller)
        window.styleMask = [.titled, .closable, .resizable]
        window.title = Copy.orchestrateBoardItem
        window.orderFront(nil)
        RunLoop.main.run(until: Date().addingTimeInterval(0.5))
        return window
    }

    private func subviews(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(subviews) }

    /// The request editor's scroll view: its document is the paste text view.
    private func editorScrolls(_ host: NSView) -> [NSScrollView] {
        subviews(host).compactMap { $0 as? NSScrollView }.filter { $0.documentView is ImagePasteTextView }
    }

    /// The form's own scroll view: it carries the worker grid's popups.
    private func formScrolls(_ host: NSView) -> [NSScrollView] {
        subviews(host).compactMap { $0 as? NSScrollView }.filter { scroll in subviews(scroll).contains { $0 is NSPopUpButton } }
    }

    func testRequestFieldPresentInStartAndHandoffModes() async throws {
        for key in ["BUG-7", "EPIC-12"] {
            let form = try await handoffForm(selectedKey: key)
            let text = try ocrText(contentHost(form, height: 1000))
            XCTAssertTrue(text.contains("Additional request (optional)"), "\(key): \(text)")
        }
    }

    func testRequestEditorHeightIs120() async throws {
        XCTAssertEqual(BoardHandoffView.requestFieldHeight, 120, "the field's fixed height")
        let form = try await handoffForm(selectedKey: "BUG-7")
        let host = contentHost(form, height: 1000)
        let editor = try XCTUnwrap(editorScrolls(host).first, "request editor present")
        // The field is the scroll view plus RequestEditor's own 12pt vertical padding.
        XCTAssertEqual(editor.frame.height, BoardHandoffView.requestFieldHeight - 12, accuracy: 1)
    }

    func testWindowHeightStaysFixedWithEmptyStrip() async throws {
        let form = try await handoffForm(selectedKey: "BUG-7")
        let window = openWindow(BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        defer { window.close() }
        // The window's minimum and maximum heights are both the content
        // height: it cannot be resized by hand and opens at that height.
        XCTAssertEqual(window.contentMinSize.height, window.contentMaxSize.height, accuracy: 1,
                       "min \(window.contentMinSize.height) vs max \(window.contentMaxSize.height)")
        XCTAssertEqual(window.contentView?.frame.height ?? 0, window.contentMaxSize.height, accuracy: 2,
                       "the window opens at the content height")
        XCTAssertGreaterThan(window.contentMaxSize.height, 400,
                             "the full form is visible, not the 200pt loading placeholder")
        XCTAssertEqual(window.contentMinSize.width, window.contentMaxSize.width, accuracy: 1,
                       "min \(window.contentMinSize.width) vs max \(window.contentMaxSize.width)")
        XCTAssertEqual(window.contentMaxSize.width, 820, accuracy: 2, "the window opens 820pt wide")
    }

    func testWindowHeightGrowsWithFileAndError() async throws {
        let plain = try await handoffForm(selectedKey: "BUG-7")
        let plainWindow = openWindow(BoardHandoffView(form: plain, onDone: {}, onCancel: {}))
        defer { plainWindow.close() }
        let filed = try await handoffForm(selectedKey: "BUG-7")
        filed.attachments.addImage(data: Data("%PDF-1.4 x".utf8), name: "notes.pdf")
        let filedWindow = openWindow(BoardHandoffView(form: filed, onDone: {}, onCancel: {}))
        defer { filedWindow.close() }
        XCTAssertGreaterThan(filedWindow.contentMaxSize.height, plainWindow.contentMaxSize.height,
                             "a file row adds height")
        XCTAssertEqual(filedWindow.contentMinSize.height, filedWindow.contentMaxSize.height, accuracy: 1,
                       "height stays fixed with a file")
    }

    func testWindowHeightStopsAtMaxFormHeight() async throws {
        let form = try await handoffForm(selectedKey: "BUG-7")
        let uncapped = openWindow(BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        defer { uncapped.close() }
        let capped = openWindow(BoardHandoffView(form: form, onDone: {}, onCancel: {}, maxFormHeight: 200))
        defer { capped.close() }
        XCTAssertLessThan(capped.contentMaxSize.height, uncapped.contentMaxSize.height,
                          "the window stops at the height limit")
        XCTAssertEqual(capped.contentMinSize.height, capped.contentMaxSize.height, accuracy: 1,
                       "the capped window stays fixed too")
    }

    func testLowMaxFormHeightCapsWindowAndScrolls() async throws {
        let form = try await handoffForm(selectedKey: "BUG-7")
        let uncapped = contentHost(form, height: 1200).fittingSize.height
        let host = contentHost(form, height: 1200, maxFormHeight: 200)
        let capped = host.fittingSize.height
        XCTAssertLessThan(capped, uncapped, "the window stops at the height limit")
        let scroll = try XCTUnwrap(formScrolls(host).first, "the form scrolls under a low limit")
        XCTAssertEqual(scroll.frame.height, 200, accuracy: 1)
    }

    // MARK: files-not-saved (unit 5)

    private func flaggedForm(selectedKey: String, handoff: Bool, client: MockDaemonClient) async throws -> BoardHandoffForm {
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = selectedKey
        form.request = "read this"
        form.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        if handoff {
            client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        } else {
            client.startResult = .success(StartedOrchestrator(
                agent: AgentNode(name: "n", kind: .claude, model: "opus", role: .orchestrator), attachmentsFailed: true))
        }
        _ = await form.primary()
        return form
    }

    func testUnsavedFilesWarningShowsForStart() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let form = try await flaggedForm(selectedKey: "BUG-7", handoff: false, client: client)
        let text = try ocrText(contentHost(form, height: 1000))
        XCTAssertTrue(text.contains("Started, but the attachments could not be saved"), text)
    }

    func testUnsavedFilesWarningShowsForHandoff() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let form = try await flaggedForm(selectedKey: "EPIC-12", handoff: true, client: client)
        let text = try ocrText(contentHost(form, height: 1000))
        XCTAssertTrue(text.contains("Handed off, but the attachments could not be saved"), text)
    }

    func testDoneFooterReplacesCaptionFooter() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        var settings = model.state.settings
        settings.maxConcurrentAgents = 1 // BUG-7 start queues, so the footer shows its caption
        func startForm() async -> BoardHandoffForm {
            let form = BoardHandoffForm(client: client, settings: settings, agents: model.state.agents,
                                        connected: true, preselectAgent: nil)
            await form.load()
            form.selectedKey = "BUG-7"
            form.request = "read this"
            return form
        }
        let plain = await startForm()
        let plainText = try ocrText(contentHost(plain, height: 1000))
        // "available" misreads under OCR; the stable fragment pins the queued caption.
        XCTAssertTrue(plainText.contains("agent slot becomes"), plainText)
        let flagged = await startForm()
        flagged.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        client.startResult = .success(StartedOrchestrator(
            agent: AgentNode(name: "n", kind: .claude, model: "opus", role: .orchestrator), attachmentsFailed: true))
        _ = await flagged.primary()
        let flaggedText = try ocrText(contentHost(flagged, height: 1000))
        XCTAssertTrue(flaggedText.contains("Started, but the attachments could not be saved"), flaggedText)
        XCTAssertFalse(flaggedText.contains("agent slot becomes"),
                       "the Done footer replaces the caption footer")
    }

    func testDoneButtonIsEnabledAndFiresAfterUpdateAndWhenDisconnected() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "EPIC-12"
        form.request = "read this"
        form.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        _ = await form.primary()
        form.update(agents: model.state.agents.filter { $0.name != "billing-spike-orchestrator" })
        form.connected = false
        XCTAssertFalse(form.canSubmit, "the primary path is dead while disconnected")
        // Offscreen SwiftUI buttons expose no NSButton to press, so drive the
        // real window's default action with Return: only an enabled Done handles it.
        var done = false
        let window = openWindow(BoardHandoffView(form: form, onDone: { done = true }, onCancel: {}))
        defer { window.close() }
        window.makeKeyAndOrderFront(nil)
        try await taskSleep(.milliseconds(300))
        let enter = try XCTUnwrap(NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
                                                   timestamp: ProcessInfo.processInfo.systemUptime,
                                                   windowNumber: window.windowNumber, context: nil,
                                                   characters: "\r", charactersIgnoringModifiers: "\r",
                                                   isARepeat: false, keyCode: 36))
        XCTAssertTrue(window.performKeyEquivalent(with: enter), "Return reaches the default Done button")
        XCTAssertTrue(done, "Done fires onDone")
    }
}

/// Offscreen SwiftUI buttons expose no measurable NSButton or accessibility frame, so the contract is pinned at
/// source level: footer action buttons stay plain native buttons (equal size); real-window screencaptures
/// verify the rendered result.
final class DialogFooterButtonTests: XCTestCase {
    private func footer(_ file: String) throws -> String {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources/SwarmBarUI/\(file)")
        let src = try String(contentsOf: url, encoding: .utf8)
        let start = try XCTUnwrap(src.range(of: "Button(Copy.cancel, action: onCancel)"))
        let end = try XCTUnwrap(src.range(of: ".padding(.horizontal, 22)", range: start.upperBound..<src.endIndex))
        return String(src[start.lowerBound..<end.lowerBound])
    }

    func testFooterActionButtonsCarryNoCustomStyle() throws {
        for file in ["NewOrchestratorView.swift", "BoardHandoffView.swift"] {
            let f = try footer(file)
            for banned in ["glassButtons", "prominentDefaultAction", "buttonStyle"] {
                XCTAssertFalse(f.contains(banned), "\(file) footer must use native buttons, found \(banned)")
            }
            XCTAssertTrue(f.contains(".keyboardShortcut(.cancelAction)") && f.contains(".keyboardShortcut(.defaultAction)"), file)
        }
    }
}
