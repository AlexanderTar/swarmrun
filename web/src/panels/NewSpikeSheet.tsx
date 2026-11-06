import { cn } from "@/lib/utils";
import { Loader2, TriangleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { RequestField } from "../components/RequestField";
import { Segmented } from "../components/Segmented";
import { Sheet } from "../components/Sheet";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { C, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { kebab } from "../logic/kebab";
import {
  type SubmitFailure,
  mapSubmitError,
  nameError,
  newRequestId,
  orchestratorsBusy,
  spikePayload,
  submitLabel,
} from "../logic/spawnForm";
import { startedToast } from "../logic/toasts";
import { useRequestAttachments } from "../logic/useRequestAttachments";
import type {
  AgentCatalogEntry,
  AgentNode,
  CreateSpikeBody,
  CreateSpikeResponse,
  Settings,
  SpikeIntent,
} from "../types";

function Form(p: {
  settings: Settings;
  catalog: AgentCatalogEntry[];
  agents: AgentNode[];
  intent?: SpikeIntent;
  onClose(): void;
  onCreated(key: string): void;
}) {
  const { connected } = useConnection();
  const toast = useToast();
  const nameId = useId();
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [intent, setIntent] = useState<SpikeIntent>(p.intent ?? "chore");
  const [repos, setRepos] = useState<string[]>([]);
  const [fields, setFields] = useState<AgentFieldsValue>(() =>
    prefill(p.settings, "orchestrator", p.catalog),
  );
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const [started, setStarted] = useState<CreateSpikeResponse>();
  const attachments = useRequestAttachments({ disabled: !!started });
  const notSaved = useRef<HTMLDivElement>(null);
  const banner = useRef<HTMLDivElement>(null);
  const done = useRef<HTMLButtonElement>(null);
  const start = useRef<HTMLButtonElement>(null);
  const create = useMutation(
    (api, body: CreateSpikeBody) => api.createSpike(body),
    ["agents", "items", "item:"],
  );
  const busy = orchestratorsBusy(p.agents, p.settings);
  const tName = name.trim();
  const tReq = attachments.request.trim();
  const clientNameErr =
    tName === ""
      ? tReq === "" && nameTouched
        ? C.nameOrRequestRequired
        : undefined
      : nameError(name);
  const nameErr = failure.name ?? clientNameErr;
  const agentChoiceValid = isValid(
    validateChoice(
      fields.choice,
      fields.advisor,
      p.catalog,
      p.settings.enabled_agents,
      "orchestrator",
    ),
  );
  const canStart =
    connected &&
    !create.pending &&
    attachments.pending === 0 &&
    !started &&
    !nameErr &&
    agentChoiceValid &&
    (tName !== "" || tReq !== "");
  const nameKebab = kebab(name);

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
    if (failure.banner && !create.pending) start.current?.focus();
  }, [failure.banner, create.pending]);

  const finish = (r: CreateSpikeResponse) => {
    toast.success(startedToast(r.agent, r.item.key));
    p.onCreated(r.item.key);
  };

  const submit = async () => {
    if (create.pending) return;
    setFailure({});
    try {
      const r = await create.run(
        await spikePayload(
          {
            name,
            intent,
            repos,
            request: attachments.request,
            images: attachments.images,
            fields,
          },
          p.settings,
          p.catalog,
          requestId,
        ),
      );
      if (r.attachments_failed) setStarted(r);
      else finish(r);
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
    }
  };

  return (
    <Sheet
      title={C.newOrchestrator}
      width={900}
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
            <Button ref={start} disabled={!canStart} onClick={() => void submit()}>
              {create.pending && <Loader2 className="animate-spin" />}
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
        <div className="space-y-1.5">
          <Label htmlFor={nameId}>{C.name}</Label>
          <Input
            id={nameId}
            value={name}
            placeholder={C.nameOptionalPlaceholder}
            onChange={(e) => {
              setName(e.target.value);
              if (e.target.value.trim() !== "") setNameTouched(true);
              setFailure((f) => ({ ...f, name: undefined }));
            }}
          />
          {nameErr ? (
            <p className="text-xs text-destructive">{nameErr}</p>
          ) : (
            nameKebab !== "" && (
              <p className="font-mono text-xs text-muted-foreground">{T.agentName(nameKebab)}</p>
            )
          )}
        </div>
        <div className="grid grid-cols-[72px_1fr] items-start gap-x-2 gap-y-1 max-sm:grid-cols-1">
          <Label className="h-7 leading-7">{C.intent}</Label>
          <Segmented<SpikeIntent>
            label={C.intent}
            value={intent}
            onChange={setIntent}
            options={[
              { value: "chore", label: C.choreIntent },
              { value: "feature", label: C.featureSpike },
              { value: "debug", label: C.debugSpike },
            ]}
          />
          <p className="col-start-2 text-xs text-muted-foreground max-sm:col-start-1">
            {intent === "chore"
              ? C.choreCaption
              : intent === "feature"
                ? C.featureCaption
                : C.debugCaption}
          </p>
        </div>
        <RepoPicker
          label={C.repositoriesOptional}
          caption={intent === "chore" ? C.choreReposCaption : C.reposCaption}
          selected={repos}
          onChange={setRepos}
        />
        <AgentFields
          value={fields}
          onChange={setFields}
          settings={p.settings}
          catalog={p.catalog}
        />
        <RequestField label={C.requestOptional} state={attachments} />
      </div>
    </Sheet>
  );
}

export function NewSpikeSheet(p: {
  intent?: SpikeIntent;
  onClose(): void;
  onCreated(key: string): void;
}) {
  const settings = useSettings();
  const catalog = useCatalog();
  const agents = useAgents();
  // A failed first load gets a message + retry. A failed refetch keeps the form.
  const err = settings.error ?? catalog.error ?? agents.error;
  const loaded = !!(settings.data && catalog.data && agents.data);
  if (err && !loaded) {
    return (
      <Sheet title={C.newOrchestrator} width={900} onClose={p.onClose}>
        <Alert variant="destructive">
          {errorText(err)}{" "}
          <Button
            variant="link"
            onClick={() => {
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
  if (!settings.data || !catalog.data || !agents.data) return null;
  return <Form settings={settings.data} catalog={catalog.data} agents={agents.data} {...p} />;
}
