import { useId } from "react";
import { C } from "../copy";
import type { useRequestAttachments } from "../logic/useRequestAttachments";
import { useMediaQuery } from "../state/media";
import { RequestImages } from "./RequestImages";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";

export function RequestField(p: {
  label: string;
  state: ReturnType<typeof useRequestAttachments>;
  disabled?: boolean;
}) {
  const id = useId();
  const finePointer = useMediaQuery("(hover: hover) and (pointer: fine)");
  const hint = finePointer
    ? /Mac|iPhone|iPad/.test(navigator.userAgent)
      ? C.requestImageHintMac
      : C.requestImageHint
    : undefined;
  return (
    <div ref={p.state.zoneRef} data-drop-zone className="space-y-1.5" {...p.state.zoneHandlers}>
      <Label htmlFor={id}>{p.label}</Label>
      <div className="relative">
        <Textarea
          id={id}
          rows={5}
          className="min-h-[108px]"
          placeholder={hint}
          value={p.state.request}
          disabled={p.disabled}
          onChange={(e) => p.state.setRequest(e.target.value)}
          onPaste={p.state.onPaste}
        />
        {p.state.dragging && (
          <div className="pointer-events-none absolute inset-0 grid place-items-center rounded-md border-2 border-dashed border-ring bg-background text-sm font-medium text-link">
            {C.dropImages}
          </div>
        )}
      </div>
      <RequestImages
        images={p.state.images}
        pending={p.state.pending}
        error={p.state.error}
        disabled={p.disabled}
        onAdd={p.state.addFiles}
        onRemove={p.state.removeImage}
      />
    </div>
  );
}
