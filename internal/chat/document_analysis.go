package chat

// Analysis references contain no original or extracted text. Instruction is
// resolved from the immutable approved tool call inside each activity.
type DocumentAnalysisInput struct {
	Scope  ApprovalScope
	Model  string
	Budget DocumentBudget
}
type DocumentBudget struct{ MaxChunks, ChunkBytes, Parallelism int }

func DefaultDocumentBudget() DocumentBudget { return DocumentBudget{32, 4000, 4} }
func (in DocumentAnalysisInput) Valid() bool {
	s := in.Scope
	return ValidModel(in.Model) && in.Budget == DefaultDocumentBudget() && ValidID(string(s.ConversationID)) && ValidID(string(s.TurnID)) && ValidID(s.ToolCallID) && ValidHash(s.ActionHash) && ValidHash(s.ContentRef) && s.ToolName == "file_upload" && s.ToolVersion == "1" && s.ToolsetVersion == WebToolset && s.PolicyRevision >= 0
}
func (in DocumentAnalysisInput) ChildID() string {
	return "document/" + in.Scope.ToolCallID + "/" + in.Scope.ActionHash
}

type DocumentAnalysisResult struct {
	ChildID   string `json:"childId"`
	Chunks    int    `json:"chunks"`
	Truncated bool   `json:"truncated"`
	Status    string `json:"status"`
}
type DocumentChunkInput struct {
	Analysis DocumentAnalysisInput
	Index    int
}
type DocumentCoverage struct {
	Index, StartByte, EndByte int
	SummaryTruncated          bool
}

// DocumentPart is durable activity-only data; it is never returned to workflow
// history. Index -1 is the extraction manifest; 0..31 are immutable summaries.
type DocumentPart struct {
	Manifest DocumentAnalysisResult
	Text     string
	Coverage DocumentCoverage
	Pages    int
	Reason   string
}
