"use client";
import { useEffect, useRef, useState } from "react";
import { api, save, saved } from "./api";
import type { Receipt } from "./contracts";

export type Control = {
  requestId: string;
  label: "Stop" | "Decision" | "Revoke";
  path: string;
  body: {
    requestId: string;
    expectedActiveTurnId?: string;
    actionHash?: string;
    decision?: "once" | "all" | "deny";
  };
  receipt?: Receipt;
  error?: string;
};
// The local journal is a delivery aid, never authority for policy or execution.
// Keep immutable payloads across remounts. Late callbacks cannot write a newer
// mounted session's journal; canonical receipts still recover it by request ID.
export function useControls(
  id: string,
  commands: Record<string, Receipt>,
  onReceipt: (r: Receipt) => void,
) {
  const key = "chat.controls." + id;
  const [controls, setControls] = useState<Control[]>([]);
  const [sending, setSending] = useState<string[]>([]);
  const [storageError, setStorageError] = useState("");
  const current = useRef<Control[]>([]);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    current.current = saved<Control[]>(key) ?? [];
    setControls(current.current);
    for (const c of current.current) if (c.receipt) onReceipt(c.receipt);
    return () => {
      alive.current = false;
    };
  }, [key]); // onReceipt is an event sink, not a journal identity.
  function commit(next: Control[], beforeDispatch = false) {
    if (!alive.current) return false;
    const durable = save(key, next);
    setStorageError(
      durable
        ? ""
        : beforeDispatch
          ? "Browser storage unavailable; new control was not sent. Restore storage and retry."
          : "Control recovery storage unavailable. This control may already be accepted; consult saved work state. The original retry identity is retained.",
    );
    if (!durable && beforeDispatch) return false;
    // A storage failure after dispatch cannot erase a known server receipt or
    // claim the operation was never sent. Reload still has the original intent.
    current.current = next;
    setControls(next);
    return true;
  }
  useEffect(() => {
    let changed = false;
    const next = current.current.map((c) => {
      const r = Object.values(commands).find(
        (r) =>
          r.requestId === c.requestId || r.commandId === c.receipt?.commandId,
      );
      if (
        !r ||
        c.receipt?.resultState === "accepted" ||
        c.receipt?.resultState === "rejected"
      )
        return c;
      if (JSON.stringify(r) === JSON.stringify(c.receipt)) return c;
      changed = true;
      return { ...c, receipt: r, error: undefined };
    });
    if (changed) commit(next);
  }, [commands]);
  async function deliver(c: Control) {
    if (sending.includes(c.requestId)) return;
    setSending((s) => [...s, c.requestId]);
    try {
      const r = await api<Receipt>(`/conversations/${id}/${c.path}`, c.body);
      if (!alive.current) return;
      onReceipt(r);
      commit(
        current.current.map((v) =>
          v.requestId !== c.requestId
            ? v
            : {
                ...v,
                receipt:
                  v.receipt?.resultState !== "pending" && v.receipt
                    ? v.receipt
                    : r,
                error: undefined,
              },
        ),
      );
    } catch (e) {
      if (alive.current)
        commit(
          current.current.map((v) =>
            v.requestId !== c.requestId || v.receipt
              ? v
              : { ...v, error: (e as Error).message },
          ),
        );
    } finally {
      if (alive.current) setSending((s) => s.filter((v) => v !== c.requestId));
    }
  }
  function submit(
    label: Control["label"],
    path: string,
    body: Omit<Control["body"], "requestId">,
  ) {
    // Do not create a second identity while the same control lane is uncertain.
    if (
      current.current.some(
        (c) =>
          c.label === label &&
          (!c.receipt || c.receipt.resultState === "pending"),
      )
    )
      return;
    const requestId = crypto.randomUUID();
    const c: Control = { requestId, label, path, body: { requestId, ...body } };
    const settled = current.current
      .filter((v) => v.receipt && v.receipt.resultState !== "pending")
      .slice(-19);
    const unresolved = current.current.filter(
      (v) => !v.receipt || v.receipt.resultState === "pending",
    );
    if (commit([...settled, ...unresolved, c], true)) void deliver(c);
  }
  const pending = (label: Control["label"]) =>
    controls.some(
      (c) =>
        c.label === label &&
        (!c.receipt || c.receipt.resultState === "pending"),
    );
  return { controls, sending, storageError, submit, retry: deliver, pending };
}
