package chat

// ContextHistory is a bounded SQL snapshot. Sequence is assistant completion
// order, never user admission order. Older is the oldest unsummarized prefix;
// Recent is newest-first. No original attachment bytes are loaded.
type ContextHistory struct {
	Model         string
	Total         int
	Summary       *Summary
	Older, Recent []Exchange
}
type ContextSummaryWrite struct {
	TurnRef
	AttemptID                         string
	PreviousVersion, PreviousBoundary int64
	Sequences                         []int64
	Summary                           Summary
}
type ContextCoverage struct {
	RecentExchanges  int    `json:"recentExchanges"`
	OmittedExchanges int    `json:"omittedExchanges"`
	SummaryVersion   int64  `json:"summaryVersion"`
	SummaryBoundary  int64  `json:"summaryBoundary"`
	SummaryFailure   string `json:"summaryFailure,omitempty"`
	SourceLimited    bool   `json:"sourceLimited"`
	OutputTruncated  bool   `json:"outputTruncated,omitempty"`
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
