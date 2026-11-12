package chat

type DocumentSection struct {
	Page  int `json:"page,omitempty"`
	Start int `json:"start"`
	End   int `json:"end"`
}
type ExtractedDocument struct {
	Text      string            `json:"text"`
	Status    string            `json:"status"`
	Reason    string            `json:"reason,omitempty"`
	Pages     int               `json:"pages"`
	Truncated bool              `json:"truncated"`
	Sections  []DocumentSection `json:"sections,omitempty"`
}
