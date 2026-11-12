package chat

import "encoding/json"

const MaxContextEvidence = 4

// Recorded authorized output is canonical history, not a guarantee of truth.
// Hashes/IDs survive excerpt clipping; arguments and result text stay out of
// final-response coverage metadata.
type ContextEvidenceSource struct {
	SourceTruncated  bool          `json:"sourceTruncated"`
	SourceError      bool          `json:"sourceError"`
	ExtractionStatus string        `json:"extractionStatus,omitempty"`
	Scope            ApprovalScope `json:"scope"`
	Sequence         int64         `json:"sequence"`
	AttachmentID     AttachmentID  `json:"attachmentId,omitempty"`
	ArgumentsHash    string        `json:"argumentsHash"`
	ResultHash       string        `json:"resultHash"`
	ExcerptBytes     int           `json:"excerptBytes"`
	ExcerptTruncated bool          `json:"excerptTruncated"`
}
type ContextEvidence struct {
	Source    ContextEvidenceSource `json:"source"`
	Arguments json.RawMessage       `json:"arguments"`
	Result    WebToolResult         `json:"result"`
}
type ContextEvidenceCoverage struct {
	Eligible     int                     `json:"eligible"`
	Included     []ContextEvidenceSource `json:"included"`
	Omitted      int                     `json:"omitted"`
	Deduplicated int                     `json:"deduplicated"`
}

// ContextHistory is a bounded SQL snapshot. Sequence is assistant completion
// order, never user admission order. Older is the oldest unsummarized prefix;
// Recent is newest-first. No original attachment bytes are loaded.
type ContextHistory struct {
	Model         string
	Total         int
	Summary       *Summary
	Older, Recent []Exchange
	Evidence      []ContextEvidence // newest completion first, bounded independently of summaries
	EvidenceTotal int
}
type ContextSummaryWrite struct {
	TurnRef
	AttemptID                         string
	PreviousVersion, PreviousBoundary int64
	Sequences                         []int64
	Summary                           Summary
}
type ContextCoverage struct {
	Evidence         *ContextEvidenceCoverage `json:"evidence,omitempty"`
	RecentExchanges  int                      `json:"recentExchanges"`
	OmittedExchanges int                      `json:"omittedExchanges"`
	SummaryVersion   int64                    `json:"summaryVersion"`
	SummaryBoundary  int64                    `json:"summaryBoundary"`
	SummaryFailure   string                   `json:"summaryFailure,omitempty"`
	SourceLimited    bool                     `json:"sourceLimited"`
	OutputTruncated  bool                     `json:"outputTruncated,omitempty"`
}
type ContextMetadata struct {
	Coverage   ContextCoverage   `json:"coverage"`
	Accounting ContextAccounting `json:"accounting"`
}
type ContextAccounting struct {
	Method       string `json:"method"`
	InputTokens  int    `json:"inputTokens"`
	ImageTokens  int    `json:"imageTokens"`
	OutputTokens int    `json:"outputTokens"`
	Reserve      int    `json:"reserve"`
	Total        int    `json:"total"`
}
