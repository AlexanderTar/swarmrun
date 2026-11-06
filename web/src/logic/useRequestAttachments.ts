import {
  type ClipboardEventHandler,
  type DragEventHandler,
  type RefObject,
  useEffect,
  useRef,
  useState,
} from "react";
import type { RequestImage } from "../components/RequestImages";
import { C, T } from "../copy";
import { addImages } from "./requestImages";

export interface DragHandlers {
  onDragEnter: DragEventHandler;
  onDragOver: DragEventHandler;
  onDragLeave: DragEventHandler;
  onDrop: DragEventHandler;
}

export interface RequestAttachments {
  request: string;
  setRequest(v: string): void;
  images: RequestImage[];
  pending: number;
  error?: string;
  addFiles(files: File[]): void;
  removeImage(id: string): void;
  onPaste: ClipboardEventHandler<HTMLTextAreaElement>;
  zoneHandlers: DragHandlers;
  dragging: boolean;
  zoneRef: RefObject<HTMLDivElement | null>;
}

const SNIFFED_TYPES = /^image\/(png|jpe?g|gif|webp|heic|heif|tiff?)$/;
const hasFiles = (e: { dataTransfer: DataTransfer | null }) =>
  Array.from(e.dataTransfer?.types ?? []).includes("Files");

export function useRequestAttachments(opts: { disabled: boolean }): RequestAttachments {
  const [request, setRequest] = useState("");
  const [images, setImages] = useState<RequestImage[]>([]);
  const [pending, setPending] = useState(0);
  const [error, setError] = useState<string>();
  const [dragging, setDragging] = useState(false);
  const imagesRef = useRef<RequestImage[]>([]);
  const queue = useRef<Promise<void>>(Promise.resolve());
  const alive = useRef(true);
  const zoneRef = useRef<HTMLDivElement>(null);
  const depth = useRef(0);
  const disabled = opts.disabled;

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // A file dropped outside the zone would make the browser open it and lose the form.
  useEffect(() => {
    const guard = (e: DragEvent) => {
      if (!hasFiles(e) || zoneRef.current?.contains(e.target as Node)) return;
      e.preventDefault();
      if (e.type === "dragover" && e.dataTransfer) e.dataTransfer.dropEffect = "none";
    };
    window.addEventListener("dragover", guard);
    window.addEventListener("drop", guard);
    return () => {
      window.removeEventListener("dragover", guard);
      window.removeEventListener("drop", guard);
    };
  }, []);

  const enqueue = (
    files: File[],
    fallbackName: string,
    usable: (n: string) => boolean = (n) => n !== "",
  ) => {
    if (disabled || files.length === 0) return;
    setPending((n) => n + files.length);
    // Fallback names are .png; an unnamed file the sniffer would not recognise must not get one.
    const named = files.map((f) => ({
      blob: f,
      name: usable(f.name) ? f.name : SNIFFED_TYPES.test(f.type) ? fallbackName : C.unnamedFileName,
    }));
    queue.current = queue.current.then(async () => {
      try {
        const r = await addImages(imagesRef.current.length, named);
        if (!alive.current) return;
        const fresh = r.added.map((a) => ({
          id: crypto.randomUUID(),
          name: a.name,
          blob: a.blob,
          url: URL.createObjectURL(a.blob),
          image: a.image,
        }));
        imagesRef.current = [...imagesRef.current, ...fresh];
        setImages(imagesRef.current);
        setError(r.error);
      } catch {
        // A file the browser cannot read (a folder dropped from Finder); keep the queue alive.
        if (alive.current) setError(T.imageUnsupported(named.map((n) => n.name).join(", ")));
      } finally {
        setPending((n) => n - files.length);
      }
    });
  };

  const addFiles = (files: File[]) => enqueue(files, C.droppedImageName);
  const removeImage = (id: string) => {
    imagesRef.current = imagesRef.current.filter((i) => i.id !== id);
    setImages(imagesRef.current);
    setError(undefined);
  };
  const onPaste: ClipboardEventHandler<HTMLTextAreaElement> = (e) => {
    const files = Array.from(e.clipboardData.files);
    if (disabled || files.length === 0) return;
    e.preventDefault();
    // Browsers name every clipboard screenshot "image.png"; a pasted file with that name is renamed too.
    enqueue(files, C.pastedImageName, (n) => n !== "" && n !== "image.png");
  };
  const zoneHandlers: DragHandlers = {
    onDragEnter: (e) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      depth.current += 1;
      setDragging(true);
    },
    onDragOver: (e) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    },
    onDragLeave: (e) => {
      if (!hasFiles(e.nativeEvent)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    },
    onDrop: (e) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      depth.current = 0;
      setDragging(false);
      addFiles(Array.from(e.dataTransfer.files));
    },
  };

  return {
    request,
    setRequest,
    images,
    pending,
    error,
    addFiles,
    removeImage,
    onPaste,
    zoneHandlers,
    dragging,
    zoneRef,
  };
}
