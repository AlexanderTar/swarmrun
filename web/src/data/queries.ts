import type { FlatItemsParams, GraphScope, Item, ItemType } from "../types";
import { useQuery } from "./hooks";

export const qk = {
  items: "items" as const,
  // Paged board pages live under the "items" prefix so every existing invalidation
  // (SSE item events, reconnect `open`, mutations) still reaches them.
  board: (q: string, type: string, status: string) =>
    `items:board:q=${q}&type=${type}&status=${status}`,
  flat: (p: FlatItemsParams) =>
    `items:flat:type=${p.type ?? ""}&q=${p.q ?? ""}&limit=${p.limit ?? ""}`,
  item: (k: string) => `item:${k}`,
  checkpoints: (k: string) => `checkpoints:${k}`,
  graph: (k: string, s: GraphScope, h: number) => `graph:${k}:${s}:${h}`,
  agents: "agents" as const,
  requests: "requests" as const,
  settings: "settings" as const,
  catalog: "catalog" as const,
  repos: (q: string) => `repos:${q}`,
  artifact: (id: string, rev?: number, section?: string) =>
    `artifact:${id}:${rev ?? ""}:${section ?? ""}`,
  advice: (name: string) => `advice:${name}`,
};

export const useItems = () => useQuery(qk.items, (a) => a.items());
export const useFlatItems = (p: FlatItemsParams | null) =>
  useQuery(p ? qk.flat(p) : null, (a) => a.flatItems(p ?? {}));

// Parent options for NewItemSheet: one flat-by-type query per eligible parent type, independent of
// the loaded board pages. Flat rejects chore as a type filter (internal/items/list.go) while board
// accepts it, so chore parents ride their own board query (limit 1000: every chore tree at once).
export interface ParentOptions {
  items: Item[];
  loaded: boolean;
  error: unknown;
  reload(): void;
}

export function useParentOptions(types: ItemType[]): ParentOptions {
  const epic = useFlatItems(types.includes("epic") ? { type: "epic" } : null);
  const story = useFlatItems(types.includes("story") ? { type: "story" } : null);
  const bug = useFlatItems(types.includes("bug") ? { type: "bug" } : null);
  const spike = useFlatItems(types.includes("spike") ? { type: "spike" } : null);
  const chores = useQuery(types.includes("chore") ? qk.board("", "chore", "") : null, (a) =>
    a.boardItems({ type: "chore", limit: 1000 }),
  );
  const byType = { epic, story, bug, spike };
  const active = (Object.keys(byType) as (keyof typeof byType)[]).filter((t) => types.includes(t));
  const items = [
    ...active.flatMap((t) => byType[t].data?.items ?? []),
    ...(types.includes("chore") ? (chores.data?.items ?? []) : []),
  ];
  return {
    items,
    loaded:
      active.every((t) => byType[t].data !== undefined) &&
      (!types.includes("chore") || chores.data !== undefined),
    error: active.map((t) => byType[t].error).find((e) => e !== undefined) ?? chores.error,
    reload: () => {
      for (const t of active) byType[t].reload();
      if (types.includes("chore")) chores.reload();
    },
  };
}
export { BOARD_PAGE, useBoardItems } from "./board";
export type { BoardFilter, BoardResult } from "./board";
export const useItemDetail = (key: string | null) =>
  useQuery(key && qk.item(key), (a) => a.item(key ?? ""));
export const useCheckpoints = (key: string | null) =>
  useQuery(key && qk.checkpoints(key), (a) => a.checkpoints(key ?? ""));
export const useGraph = (key: string | null, scope: GraphScope, hops: number) =>
  useQuery(key && qk.graph(key, scope, hops), (a) => a.graph(key ?? "", scope, hops));
export const useAgents = () => useQuery(qk.agents, (a) => a.agents("active"));
export const useRequests = () => useQuery(qk.requests, (a) => a.requests());
export const useSettings = () => useQuery(qk.settings, (a) => a.settings());
export const useCatalog = () => useQuery(qk.catalog, (a) => a.catalog());
export const useRepos = (q: string) => useQuery(qk.repos(q), (a) => a.repos(q));
export const useArtifact = (id: string | null, rev?: number, section?: string) =>
  useQuery(id && qk.artifact(id, rev, section), (a) =>
    a.artifact(id ?? "", { revision: rev, section }),
  );
export const useAdvice = (name: string | null) =>
  useQuery(name && qk.advice(name), (a) => a.advice(name ?? ""));
