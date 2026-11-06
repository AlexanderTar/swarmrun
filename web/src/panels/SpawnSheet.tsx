import { cn } from "@/lib/utils";
import { Loader2, TriangleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { RequestField } from "../components/RequestField";
import { Sheet } from "../components/Sheet";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { C } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useItemDetail, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { defaultOrchestratorName } from "../logic/kebab";
import {
  type SubmitFailure,
  mapSubmitError,
  nameError,
  newRequestId,
  orchestratorPayload,
  orchestratorsBusy,
  submitLabel,
} from "../logic/spawnForm";
import { startedToast } from "../logic/toasts";
import { useRequestAttachments } from "../logic/useRequestAttachments";
import type { AgentCatalogEntry, AgentNode, Item, Settings, StartOrchestratorBody } from "../types";

function Form(p: {
  item: Item;
  settings: Settings;
  catalog: AgentCatalogEntry[];
  agents: AgentNode[];
  onClose(): void;
}) {
  const { connected } = useConnection();
  const toast = useToast();
  const nameId = useId();
  const submitting = useRef(false);
  const [repos, setRepos] = useState(p.item.repos);
  const [fields, setFields] = useState<AgentFieldsValue>(() =>
    prefill(p.settings, "orchestrator", p.catalog),
  );
  const [name, setName] = useState(() => defaultOrchestratorName(p.item.title));
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const [started, setStarted] = useState<AgentNode & { attachments_failed?: boolean }>();
  const attachments = useRequestAttachments({ disabled: !!started });
  const notSaved = useRef<HTMLDivElement>(null);
  const banner = useRef<HTMLDivElement>(null);
  const done = useRef<HTMLButtonElement>(null);
  const startButton = useRef<HTMLButtonElement>(null);
  const start = useMutation(
    (api, body: StartOrchestratorBody) => api.startOrchestrator(p.item.key, body),
    ["agents", "item:", "items"],
  );
  const busy = orchestratorsBusy(p.agents, p.settings);
  const nameErr = failure.name ?? nameError(name);
  const valid =
    !nameError(name) &&
    isValid(
      validateChoice(
        fields.choice,
        fields.advisor,
        p.catalog,
        p.settings.enabled_agents,
        "orchestrator",
      ),
    );

  const submit = async () => {
    if (submitting.current) return;
    submitting.current = true;
    setFailure({});
    try {
      const agent = await start.run(
        await orchestratorPayload(
          { name, repos, fields, request: attachments.request, images: attachments.images },
          p.item,
          p.settings,
          p.catalog,
          requestId,
        ),
      );
      if (agent.attachments_failed) setStarted(agent);
      else finish(agent);
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
      submitting.current = false;
    }
  };

  const finish = (agent: AgentNode) => {
    toast.success(startedToast(agent, p.item.key));
    p.onClose();
  };

  // The alerts are the first children of the scrolling sheet body; bring them into view.
  useEffect(() => {
    const body = (
      started ? notSaved.current : failure.banner ? banner.current : null
    )?.closest<HTMLElement>(".overflow-y-auto");
    if (body) body.scrollTop = 0;
  }, [started, failure.banner]);

  // Start unmounts when the launch lands, and the dialog's focus scope then reclaims focus; take it back for Done.
  useEffect(() => {
    if (started) done.current?.focus();
  }, [started]);

  // The disabled Start button drops focus while the request is in flight; hand it to Try again once the failure lands.
  useEffect(() => {
    if (failure.banner && !start.pending) startButton.current?.focus();
  }, [failure.banner, start.pending]);

  return (
    <Sheet
      title={C.startOrchestrator}
      width={900}
      subtitle={`${p.item.key} · ${p.item.title}`}
      onClose={started ? () => finish(started) : p.onClose}
      footer={
        <>
          {busy && (
            <span className="mr-auto self-center text-xs text-muted-foreground max-sm:order-last max-sm:mr-0 max-sm:self-start">
              {C.queuedCaption}
            </span>
          )}
          <Button variant="secondary" disabled={!!started} onClick={p.onClose}>
            {C.cancel}
          </Button>
          {started ? (
            <Button ref={done} onClick={() => finish(started)}>
              {C.done}
            </Button>
          ) : (
            <Button
              ref={startButton}
              disabled={!valid || !connected || start.pending || attachments.pending > 0}
              onClick={() => void submit()}
            >
              {start.pending && <Loader2 className="animate-spin" />}
              {failure.banner ? C.tryAgain : submitLabel(busy)}
            </Button>
          )}
        </>
      }
    >
      {started && (
        <Alert ref={notSaved} className="border-warning/40 text-warning">
          <TriangleAlert />
          <div>{C.imagesNotSaved}</div>
        </Alert>
      )}
      {failure.banner && (
        <Alert ref={banner} variant="destructive" role="alert">
          <TriangleAlert />
          <div>
            <p>{failure.banner}</p>
            {failure.detail && <p className="mt-1">{failure.detail}</p>}
          </div>
        </Alert>
      )}
      <div
        className={cn("space-y-4", started && "opacity-50")}
        inert={!!started}
        aria-disabled={!!started}
        data-started={!!started}
      >
        <RepoPicker label={C.repositories} selected={repos} onChange={setRepos} />
        <AgentFields
          value={fields}
          onChange={setFields}
          settings={p.settings}
          catalog={p.catalog}
        />
        <div className="space-y-1.5">
          <Label htmlFor={nameId}>{C.name}</Label>
          <Input
            id={nameId}
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setFailure((f) => ({ ...f, name: undefined }));
            }}
            className="key"
          />
          {name !== "" && nameErr && <p className="text-xs text-destructive">{nameErr}</p>}
        </div>
        <RequestField label={C.additionalRequest} state={attachments} />
      </div>
    </Sheet>
  );
}

export function SpawnSheet(p: { itemKey: string; onClose(): void }) {
  const detail = useItemDetail(p.itemKey);
  const settings = useSettings();
  const catalog = useCatalog();
  const agents = useAgents();
  // A failed first load gets a message + retry. A failed refetch keeps the form: the store
  // leaves the last data in place, and replacing it would drop the request, files and Done.
  const err = detail.error ?? settings.error ?? catalog.error ?? agents.error;
  const loaded = !!(detail.data && settings.data && catalog.data && agents.data);
  if (err && !loaded) {
    return (
      <Sheet title={C.startOrchestrator} width={900} onClose={p.onClose}>
        <Alert variant="destructive">
          {errorText(err)}{" "}
          <Button
            variant="link"
            onClick={() => {
              detail.reload();
              settings.reload();
              catalog.reload();
              agents.reload();
            }}
          >
            {C.retry}
          </Button>
        </Alert>
      </Sheet>
    );
  }
  if (!detail.data || !settings.data || !catalog.data || !agents.data) return null;
  return (
    <Form
      item={detail.data.item}
      settings={settings.data}
      catalog={catalog.data}
      agents={agents.data}
      onClose={p.onClose}
    />
  );
}
