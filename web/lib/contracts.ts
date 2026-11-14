export type Model = "qwen3.5:9b" | "gemma4:e4b";
export type Conversation = {
  id: string;
  title: string;
  model: Model;
  workflowId: string;
  lifecycle: "open" | "closed";
  policyRevision: number;
  createdAt: string;
  updatedAt: string;
};
export type Receipt = {
  commandId: string;
  requestId?: string;
  turnId: string | null;
  payloadDigest: string;
  dispatchState: "pending" | "dispatched";
  resultState: "pending" | "accepted" | "rejected";
  result: { state?: string; code?: string } | null;
};
export type ContextMetadata = {
  coverage: {
    recentExchanges: number;
    omittedExchanges: number;
    summaryVersion: number;
    summaryBoundary: number;
    summaryFailure?: string;
    sourceLimited: boolean;
    outputTruncated?: boolean;
    evidence?: {
      eligible: number;
      included: {
        excerptTruncated: boolean;
        sourceTruncated?: boolean;
        sourceError?: boolean;
        extractionStatus?: string;
      }[];
      omitted: number;
      deduplicated: number;
    };
    quotations?: {
      eligible: number;
      included: unknown[];
      omitted: number;
      deduplicated: number;
    };
  };
  accounting: {
    method: string;
    inputTokens: number;
    imageTokens: number;
    outputTokens: number;
    reserve: number;
    total: number;
  };
};
export type Message = {
  entryId: string;
  conversationId: string;
  turnId: string;
  role: "user" | "assistant";
  text: string;
  attachmentIds: string[] | null;
  logicalStep: number;
  resultKind: string;
  completionState: "completed" | "incomplete";
  context: ContextMetadata | null;
  turnStatus?: string;
  turnReason?: string;
  sequence: number;
  createdAt: string;
};
export type MessagePage = { messages: Message[]; before: string };
export type Turn = {
  id: string;
  status:
    | "queued"
    | "running"
    | "awaiting_approval"
    | "cancelling"
    | "completed"
    | "cancelled"
    | "failed";
  reason: string;
};
export type Metadata = {
  conversation: Conversation;
  activeTurn: { id: string; status: string; updatedAt: string } | null;
  pendingApproval: {
    id: string;
    toolCallId: string;
    toolName: string;
    arguments: Record<string, unknown>;
    contentRef: string;
    actionHash: string;
    deadline: string;
    policyRevision: number;
    toolsetVersion: string;
  } | null;
  policy: { revision: number; approvalAll: boolean; toolsetVersion: string };
  freshness: {
    source: "postgres_projection";
    authoritative: false;
    observedAt: string;
    workflowConfirmedAt: null;
  };
  dispatcherConnected: boolean;
};
export type Tool = {
  id: string;
  turnId: string;
  name: string;
  status: string;
  result: Record<string, unknown> | null;
  sources: unknown;
};
export type Attempt = {
  turnId: string;
  stepId: number;
  attempt: number;
  status: string;
};
export type Snapshot = MessagePage & {
  reset: boolean;
  metadata: Metadata;
  turns: Turn[];
  commands: Receipt[];
  tools: Tool[];
  attempts: Attempt[];
};
export type Envelope = {
  id: string;
  conversationId: string;
  turnId: string | null;
  stepId: number | null;
  attempt: number | null;
} & (
  | { type: "snapshot"; data: Snapshot }
  | { type: "message.delta"; data: { text: string; bounded?: boolean } }
  | { type: "message.persisted"; data: Message }
  | { type: "command.result"; data: Receipt }
  | { type: "turn.status"; data: Turn }
  | { type: "tool.state"; data: Tool }
  | { type: "approval.state"; data: Metadata["pendingApproval"] }
  | { type: "policy.state"; data: Metadata["policy"] }
);
export type ConversationPage = {
  conversations: Conversation[];
  cursor: string;
};
export const attachmentErrorCodes = [
  "attachment_store_unavailable",
  "multipart_required",
  "one_file_required",
  "file_exceeds_1048576_bytes",
  "invalid_signature_or_unreadable_file",
] as const;
export type APIErrorCode =
  | (typeof attachmentErrorCodes)[number]
  | "forbidden"
  | "invalid_request"
  | "invalid_json"
  | "invalid_query"
  | "not_found"
  | "reference_not_found"
  | "conflict"
  | "body_too_large"
  | "prompt_too_large"
  | "unsupported_media_type"
  | "storage_unavailable"
  | "not_implemented"
  | "method_not_allowed"
  | "upstream_unavailable";
