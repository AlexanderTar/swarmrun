import XCTest
@testable import SwarmBarKit

@MainActor
final class BoardHandoffTests: XCTestCase {
    var client: MockDaemonClient!
    var state = StateResponse()
    var catalog: [AgentCatalogEntry] = []

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        state = try Fixture.decode("state.json")
        catalog = try Fixture.decode("catalog.json")
    }

    private func form(preselect: String? = nil, connected: Bool = true, max: Int? = nil) async -> BoardHandoffForm {
        var settings = state.settings
        if let max { settings.maxConcurrentAgents = max }
        let f = BoardHandoffForm(client: client, settings: settings, agents: state.agents, connected: connected, preselectAgent: preselect)
        await f.load()
        return f
    }

    func testLoadingShowsNoWorkerErrors() async {
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: state.agents, connected: true, preselectAgent: nil)
        XCTAssertTrue(f.loading)
        for role in BoardHandoffForm.workerRoles {
            let e = f.workerErrors(role)
            XCTAssertNil(e.agent, "no agent error while the catalog is still loading (\(role))")
            XCTAssertNil(e.model, "no model error while the catalog is still loading (\(role))")
        }
        await f.load()
        XCTAssertFalse(f.loading)
    }

    func testRowsEligibilityAndOrdering() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(rows.map(\.id), ["SPIKE-4", "EPIC-12", "BUG-7", "CHORE-3"],
                       "live-orchestrator rows first, then Ready; daemon order within; no story/task/child/done/in-progress-without-orchestrator/unknown type")
        XCTAssertEqual(rows[1].orchestrator?.name, "auth-epic-orchestrator")
        XCTAssertNil(rows[2].orchestrator)
    }

    func testDraftChoreIsListedAfterOrchestratedRows() {
        let items = client.boardItemList + [
            BoardItem(key: "CHORE-9", type: "chore", status: "draft", title: "Tidy"),
            BoardItem(key: "CHORE-10", type: "chore", status: "done", title: "Old"),
            BoardItem(key: "CHORE-11", type: "chore", status: "cancelled", title: "Nope"),
            BoardItem(key: "BUG-30", type: "bug", status: "in_progress", title: "Busy"),
            BoardItem(key: "CHORE-12", type: "chore", status: "draft", title: "Child", parentKey: "EPIC-12"),
        ]
        let rows = BoardHandoffRules.rows(items, agents: state.agents)
        XCTAssertEqual(rows.map(\.id), ["SPIKE-4", "EPIC-12", "BUG-7", "CHORE-3", "CHORE-9"])
        XCTAssertEqual(BoardHandoffRules.detail(rows[4], catalog: catalog), "Chore · Draft")
    }

    func testDraftItemStartsLikeReady() async {
        client.boardItemList = [BoardItem(key: "CHORE-9", type: "chore", status: "draft", title: "Tidy")]
        let f = await form()
        XCTAssertEqual(f.selectedKey, "CHORE-9")
        XCTAssertFalse(f.isHandoff)
        f.update(agents: state.agents.filter { $0.name != "billing-spike-orchestrator" }) // a queued one forces "Queue"
        XCTAssertEqual(f.primaryLabel, Copy.startOrchestrator)
    }

    func testFinishedOrchestratorDoesNotCount() {
        var agents = state.agents
        agents[0].state = .finished // auth-epic-orchestrator
        XCTAssertFalse(BoardHandoffRules.rows(client.boardItemList, agents: agents).contains { $0.id == "EPIC-12" })
    }

    func testTitleAndDetail() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(BoardHandoffRules.title(rows[1]), "EPIC-12 · Authentication")
        let o = rows[1].orchestrator!
        let entry = CatalogRules.entry(catalog, o.kind)
        let parts = [o.name, CatalogRules.modelLabel(entry, o.model), CatalogRules.previewEffortLabel(entry, o.model, o.effort),
                     AgentTree.handoffStatus(o) ?? DisplayState(o).label ?? Copy.runningLabel].compactMap { $0 }
        XCTAssertEqual(BoardHandoffRules.detail(rows[1], catalog: catalog), parts.joined(separator: " · "))
        XCTAssertEqual(BoardHandoffRules.detail(rows[2], catalog: catalog), "Bug · Ready")
        XCTAssertEqual(BoardHandoffRules.detail(rows[3], catalog: catalog), "Chore · Ready")
    }

    func testDetailOmitsUnknownEffort() {
        var o = AgentNode(name: "x-orch", kind: .claude, model: "unknown-model", role: .orchestrator, itemKey: "BUG-7")
        o.effort = nil
        let row = BoardItemRow(item: client.boardItemList[0], orchestrator: o)
        XCTAssertEqual(BoardHandoffRules.detail(row, catalog: catalog).components(separatedBy: " · ").count, 3)
    }

    func testCanHandOff() {
        let flat = AgentTree.flatten(state.agents)
        let running = flat.first { $0.name == "auth-epic-orchestrator" }!
        let queued = flat.first { $0.name == "billing-spike-orchestrator" }!
        XCTAssertTrue(BoardHandoffRules.canHandOff(running, connected: true))
        XCTAssertFalse(BoardHandoffRules.canHandOff(running, connected: false))
        XCTAssertFalse(BoardHandoffRules.canHandOff(queued, connected: true))
        var replacing = running
        replacing.replacement = AgentReplacement(operationID: "op", mode: "handoff", phase: "stopping")
        XCTAssertFalse(BoardHandoffRules.canHandOff(replacing, connected: true))
    }

    func testOffersHandOffToOnlyOnTopLevelOrchestratorsWithEnabledHandoff() {
        let flat = AgentTree.flatten(state.agents)
        let orch = flat.first { $0.name == "auth-epic-orchestrator" }!
        let coder = flat.first { $0.name == "login-form-coder" }!
        XCTAssertTrue(BoardHandoffRules.offersHandOffTo(orch, actions: AgentTree.actions(orch, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(coder, actions: AgentTree.actions(coder, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(orch, actions: []))
    }

    func testDefaultSelectionAndLabels() async {
        let f = await form()
        XCTAssertEqual(f.selectedKey, "SPIKE-4", "first row when nothing is preselected")
        XCTAssertTrue(f.isHandoff)
        XCTAssertEqual(f.primaryLabel, Copy.handOff)
        XCTAssertEqual(f.caption, Copy.handoffUnavailable, "queued orchestrator can't hand off")
        XCTAssertFalse(f.canSubmit)
        f.selectedKey = "EPIC-12"
        XCTAssertNil(f.caption)
        XCTAssertTrue(f.canSubmit)
        // state.json holds a queued orchestrator, so wouldQueue is true until it is gone.
        f.update(agents: state.agents.filter { $0.name != "billing-spike-orchestrator" })
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.startOrchestrator)
    }

    func testQueueLabelAtCapacity() async {
        let f = await form(max: 1)
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.queueOrchestrator)
        XCTAssertEqual(f.caption, Copy.queuedCaption)
    }

    func testPreselectPicksTheOrchestratorsItem() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        XCTAssertEqual(f.selectedKey, "EPIC-12")
    }

    func testHandoffSubmitSendsPicksAndReusesRequestIDOnRetry() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.picker.setAgent("codex")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.failure?.hasPrefix(Copy.handoffFailed) == true)
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId, "same entries → same id")
        XCTAssertEqual(client.handoffRequests[1].agent, .codex)
        XCTAssertEqual(client.calls.last, "handoff auth-epic-orchestrator codex \(f.picker.choice.model)")
    }

    func testApiErrorMintsNewRequestID() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        client.failNext = .api(status: 409, code: "conflict", message: "A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertEqual(f.failure, Copy.handoffFailed + " A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertNotEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
    }

    func testStartAndHandoffBodiesCarryEntriesOnlyWhenPresent() async throws {
        let handoff = await form(preselect: "auth-epic-orchestrator")
        handoff.request = "read this"
        handoff.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        let handoffClosed = await handoff.primary()
        XCTAssertTrue(handoffClosed)
        let sentHandoff = try XCTUnwrap(client.handoffRequests.last)
        XCTAssertEqual(sentHandoff.request, "read this")
        XCTAssertEqual(sentHandoff.attachments, [AttachmentPayload(name: "a.txt", data: Data("x".utf8).base64EncodedString())])

        let start = await form()
        start.selectedKey = "CHORE-3"
        start.request = "  "
        let startClosed = await start.primary()
        XCTAssertTrue(startClosed)
        let sentStart = try XCTUnwrap(client.startBodies.last)
        XCTAssertNil(sentStart.request)
        XCTAssertNil(sentStart.attachments)
    }

    func testRetryAfterEditingRequestUsesNewRequestID() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        f.request = "read this"
        let second = await f.primary()
        XCTAssertTrue(second)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertNotEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
        XCTAssertEqual(try XCTUnwrap(client.handoffRequests.last).request, "read this")
    }

    func testRetryAfterEditingFilesUsesNewRequestID() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        let second = await f.primary()
        XCTAssertTrue(second)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertNotEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
        XCTAssertEqual(try XCTUnwrap(client.handoffRequests.last).attachments?.map(\.name), ["a.txt"])
    }

    func testUnchangedRetryWithEntriesReusesRequestID() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "read this"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        let second = await f.primary()
        XCTAssertTrue(second)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
        XCTAssertEqual(try XCTUnwrap(client.handoffRequests.last).request, "read this")
    }

    func testReadyStartUsesStartEndpoint() async {
        let f = await form()
        f.selectedKey = "CHORE-3"
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.startBodies.count, 1)
        XCTAssertTrue(client.calls.contains { $0.hasPrefix("start CHORE-3 claude") })
    }

    func testLoadFailureShowsTryAgainAndReloads() async {
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: state.agents, connected: true, preselectAgent: nil)
        client.boardItemsError = .unreachable
        await f.load()
        XCTAssertEqual(f.loadError, Copy.boardItemsLoadFailed)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.canSubmit, "Try again reruns load()")
        client.boardItemsError = nil
        let closed = await f.primary()
        XCTAssertFalse(closed)
        XCTAssertNil(f.loadError)
        XCTAssertEqual(f.rows.count, 4)
    }

    func testEmptyListDisablesPrimary() async {
        client.boardItemList = []
        let f = await form()
        XCTAssertNil(f.selected)
        XCTAssertFalse(f.canSubmit)
    }

    func testUpdateKeepsSelectionWhileEligible() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        var agents = state.agents
        f.update(agents: agents)
        XCTAssertEqual(f.selectedKey, "EPIC-12")
        agents[0].state = .finished
        f.update(agents: agents)
        XCTAssertNotEqual(f.selectedKey, "EPIC-12", "EPIC-12 is in progress without a live orchestrator → gone")
    }

    func testLoadWithMissingRowKeepsNilWhenEntriesExist() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        f.request = "keep me"
        client.boardItemList = client.boardItemList.filter { $0.key != "BUG-7" }
        await f.load()
        XCTAssertNil(f.selectedKey, "entries must not jump to another item on reload")
        XCTAssertEqual(f.request, "keep me")
    }

    func testLoadWithMissingRowJumpsWhenNoEntries() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        client.boardItemList = client.boardItemList.filter { $0.key != "BUG-7" }
        await f.load()
        XCTAssertEqual(f.selectedKey, f.rows.first?.id, "no entries: reload jumps as it always has")
    }

    func testSelectHandoffAgentClearsWhenStillMissing() async {
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        form.request = "keep me"
        // BUG-7 leaves the board, then Hand off to… names an agent that is not there.
        client.boardItemList = client.boardItemList.filter { $0.key != "BUG-7" }
        await model.selectHandoffAgent("no-such-orch", on: form)
        XCTAssertNil(form.selectedKey, "an unknown agent must not park entries on another item")
        XCTAssertEqual(form.request, "keep me")
    }

    func testSelectingAnotherItemKeepsEntries() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "keep me"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.request, "keep me")
        XCTAssertEqual(f.attachments.images.map(\.name), ["a.txt"])
    }

    func testUpdateWithSelectedRowGoneClearsSelectionWhenEntriesExist() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "keep me"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        var agents = state.agents
        agents[0].state = .finished // auth-epic-orchestrator gone: EPIC-12 leaves the list
        f.update(agents: agents)
        XCTAssertNil(f.selectedKey, "ineligible selection clears instead of jumping")
        XCTAssertFalse(f.canSubmit, "Start stays disabled until another item is picked")
        XCTAssertEqual(f.request, "keep me")
        XCTAssertEqual(f.attachments.images.map(\.name), ["a.txt"])
    }

    func testUpdateWithSelectedRowGoneJumpsWhenNoEntries() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        var agents = state.agents
        agents[0].state = .finished
        f.update(agents: agents)
        XCTAssertEqual(f.selectedKey, f.rows.first?.id, "no entries: today's jump to the first row")
    }

    func testStartToHandoffFlipKeepsEntries() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        XCTAssertFalse(f.isHandoff)
        f.request = "keep me"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        var agents = state.agents
        agents.append(AgentNode(name: "bug7-orch", kind: .claude, model: "opus", role: .orchestrator,
                                itemKey: "BUG-7", itemTitle: "Login crash", rootKey: "BUG-7",
                                state: .active, session: SessionInfo(state: .running)))
        f.update(agents: agents)
        XCTAssertEqual(f.selectedKey, "BUG-7")
        XCTAssertTrue(f.isHandoff)
        XCTAssertEqual(f.request, "keep me")
        XCTAssertEqual(f.attachments.images.map(\.name), ["a.txt"])
    }

    func testHandOffToWhileOpenSelectsWithoutRebuildAndClearsPreselect() async throws {
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        form.request = "keep me"
        form.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        await model.selectHandoffAgent("auth-epic-orchestrator", on: form)
        XCTAssertNil(model.boardHandoffPreselect)
        XCTAssertEqual(form.selectedKey, "EPIC-12")
        XCTAssertEqual(form.request, "keep me", "entries survive: the form is not rebuilt")
        XCTAssertEqual(form.attachments.images.map(\.name), ["a.txt"])
    }

    func testHandOffToWhileOpenReloadsWhenTargetIsMissing() async throws {
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.request = "keep me"
        XCTAssertFalse(form.selectAgent("fresh-orch"), "target item is not in the form's list yet")
        client.boardItemList.append(BoardItem(key: "CHORE-20", type: "chore", status: "ready", title: "Fresh"))
        var st = try client.stateResult.get()
        st.agents.append(AgentNode(name: "fresh-orch", kind: .claude, model: "opus", role: .orchestrator,
                                   itemKey: "CHORE-20", itemTitle: "Fresh", rootKey: "CHORE-20",
                                   state: .active, session: SessionInfo(state: .running)))
        client.stateResult = .success(st)
        await model.refresh()
        model.boardHandoffPreselect = "fresh-orch"
        await model.selectHandoffAgent("fresh-orch", on: form)
        XCTAssertNil(model.boardHandoffPreselect)
        XCTAssertEqual(form.selectedKey, "CHORE-20")
        XCTAssertEqual(form.request, "keep me", "entries survive the reload")
    }

    func testPreselectRebuildsWhenUnsavedNoticeSet() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "read this"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        XCTAssertFalse(f.shouldRebuildForPreselect, "entries move instead of rebuilding")
        client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        _ = await f.primary()
        XCTAssertNotNil(f.unsavedFilesNotice, "Done state is set")
        XCTAssertTrue(f.shouldRebuildForPreselect,
                      "with the files-not-saved notice a new preselection rebuilds rather than moving the inert form's selection")
    }

    func testAttachmentsFailedOnStartSetsNoticeWithStartWording() async throws {
        let f = await form()
        f.selectedKey = "BUG-7"
        f.request = "read this"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        client.startResult = .success(StartedOrchestrator(
            agent: AgentNode(name: "n", kind: .claude, model: "opus", role: .orchestrator), attachmentsFailed: true))
        let closed = await f.primary()
        XCTAssertFalse(closed, "the window stays open for Done")
        XCTAssertEqual(f.unsavedFilesNotice,
                       "Started, but the attachments could not be saved. Share them with the orchestrator another way.")
    }

    func testAttachmentsFailedOnHandoffSetsNoticeWithHandoffWording() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "read this"
        f.attachments.addImage(data: Data("x".utf8), name: "a.txt")
        client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        let closed = await f.primary()
        XCTAssertFalse(closed, "the window stays open for Done")
        XCTAssertEqual(f.unsavedFilesNotice,
                       "Handed off, but the attachments could not be saved. Share them with the orchestrator another way.")
    }

    func testUnsavedNoticeHoldsAfterAgentsUpdateChangesTheRow() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "read this"
        client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        _ = await f.primary()
        let notice = try XCTUnwrap(f.unsavedFilesNotice)
        // Another row's orchestrator finishes: the rows change under the notice.
        f.update(agents: state.agents.filter { $0.name != "billing-spike-orchestrator" })
        XCTAssertEqual(f.unsavedFilesNotice, notice)
    }

    func testDoneStaysAvailableAfterUpdateAndWhenDisconnected() async throws {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.request = "read this"
        client.handoffResult = .success(HandoffResult(attachmentsFailed: true))
        _ = await f.primary()
        f.update(agents: state.agents.filter { $0.name != "billing-spike-orchestrator" })
        f.connected = false
        XCTAssertFalse(f.canSubmit, "the primary path is dead while disconnected")
        XCTAssertNotNil(f.unsavedFilesNotice, "Done stays available: it never reads canSubmit or connected")
    }

    func testAppModelPreselectIsConsumedOnce() async {
        let model = makeAppModel(client)
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        _ = model.makeBoardHandoffForm()
        XCTAssertNil(model.boardHandoffPreselect)
    }

    func testWorkerRolesCoverEveryWorkerAndNeverOrchestratorOrAdvisor() {
        XCTAssertEqual(BoardHandoffForm.workerRoles, [.coder, .reviewer, .uiReviewer, .researcher, .debugger, .mechanical, .designer])
    }

    func testWorkerPicksPrefillFromSettings() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        XCTAssertFalse(f.isHandoff)
        XCTAssertEqual(f.workerChoice(.coder), AgentChoice(agent: .claude, model: "sonnet"))
        XCTAssertEqual(f.workerChoice(.reviewer), AgentChoice(agent: .codex, model: "gpt-6-astra", effort: "high"))
    }

    func testWorkerRolesPayloadSendsOnlyChangedRoles() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        XCTAssertNil(f.workerRolesPayload, "unchanged defaults keep following Settings")
        f.setWorkerAgent(.coder, "codex")
        XCTAssertEqual(f.workerRolesPayload?.keys.sorted(), ["coder"])
        XCTAssertEqual(f.workerRolesPayload?["coder"]?.agent, .codex)
    }

    func testWorkerRolesPayloadSendsEffortOnlyChange() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        f.setWorkerEffort(.reviewer, "low")
        XCTAssertEqual(f.workerRolesPayload?.keys.sorted(), ["reviewer"], "effort-only change is still a change")
        XCTAssertEqual(f.workerRolesPayload?["reviewer"]?.effort, "low")
    }

    func testWorkerRolesPayloadOmitsRoleChangedBackToDefault() async {
        let f = await form()
        f.selectedKey = "BUG-7"
        f.setWorkerEffort(.reviewer, "low")
        XCTAssertNotNil(f.workerRolesPayload)
        f.setWorkerEffort(.reviewer, "high")
        XCTAssertNil(f.workerRolesPayload, "changing back to the Settings default omits the role again")
    }

    func testWorkerSettersWorkWhenSettingsLacksRoleDefault() async {
        var settings = state.settings
        settings[.coder] = nil
        let f = BoardHandoffForm(client: client, settings: settings, agents: state.agents, connected: true, preselectAgent: nil)
        await f.load()
        f.selectedKey = "BUG-7"
        f.setWorkerAgent(.coder, "codex")
        XCTAssertEqual(f.workerChoice(.coder).agent, .codex, "picker edits apply even with no Settings default")
    }

    func testHandoffWorkersPrefillFromOrchestratorOverridesThenSettings() async {
        var agents = state.agents
        let i = agents.firstIndex { $0.name == "auth-epic-orchestrator" }!
        agents[i].roleOverrides = ["coder": RoleDefault(agent: .codex, model: "gpt-6-astra", effort: "low")]
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: agents, connected: true, preselectAgent: "auth-epic-orchestrator")
        await f.load()
        XCTAssertTrue(f.isHandoff)
        XCTAssertEqual(f.workerChoice(.coder).agent, .codex, "override wins")
        XCTAssertEqual(f.workerChoice(.coder).model, "gpt-6-astra")
        XCTAssertEqual(f.workerChoice(.reviewer).agent, state.settings[.reviewer]?.agent, "no override falls back to Settings")
        XCTAssertNil(f.workerRolesPayload, "prefill alone is not a change")
    }

    func testHandoffSendsOnlyChangedRoles() async throws {
        var agents = state.agents
        let i = agents.firstIndex { $0.name == "auth-epic-orchestrator" }!
        agents[i].roleOverrides = ["coder": RoleDefault(agent: .codex, model: "gpt-6-astra", effort: "low")]
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: agents, connected: true, preselectAgent: "auth-epic-orchestrator")
        await f.load()
        f.setWorkerEffort(.coder, "high")
        let closed = await f.primary()
        XCTAssertTrue(closed)
        let sent = try XCTUnwrap(client.handoffRequests.last)
        XCTAssertEqual(sent.roles?.keys.sorted(), ["coder"])
        XCTAssertEqual(sent.roles?["coder"]?.effort, "high")
    }

    func testHandoffBlockedByInvalidWorkerPick() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        XCTAssertTrue(f.canSubmit)
        f.setWorkerModel(.coder, "no-such-model")
        XCTAssertFalse(f.workersValid)
        XCTAssertFalse(f.canSubmit, "an invalid worker row must block Hand off, not surface as a daemon 4xx")
    }

    func testSwitchingRowsDropsWorkerEditsMadeAgainstTheOldBaseline() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        let handoffKey = f.selectedKey
        f.setWorkerEffort(.coder, "high")
        let other = f.rows.first { $0.orchestrator == nil }
        guard let other else { return XCTFail("fixture needs a start row") }
        f.selectedKey = other.id
        f.selectedKey = handoffKey
        XCTAssertNil(f.workerRolesPayload, "edits belonged to the previous selection's baseline")
    }

    func testHandoffWithoutWorkerEditsSendsNoRoles() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        _ = await f.primary()
        XCTAssertNil(client.handoffRequests.last?.roles)
    }

    func testAgentNodeDecodesRoleOverrides() throws {
        let json = #"{"id":"a","name":"n","kind":"claude","model":"m","role":"orchestrator","item_key":"K","item_title":"T","root_key":"K","state":"active","children":[],"finished":[],"role_overrides":{"coder":{"agent":"codex","model":"x","effort":"low"}}}"#
        let n = try JSONDecoder().decode(AgentNode.self, from: Data(json.utf8))
        XCTAssertEqual(n.roleOverrides?["coder"]?.agent, .codex)
    }
}
