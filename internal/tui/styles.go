package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var identityStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("80"))
var attentionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))

func semanticHeading(text, tone string) string {
	style := lipgloss.NewStyle()
	switch tone {
	case "user":
		style = identityStyle
	case "complete":
		style = style.Bold(true).Foreground(lipgloss.Color("78"))
	case "failure":
		style = style.Bold(true).Foreground(lipgloss.Color("203"))
	case "attention":
		style = attentionStyle
	case "muted":
		style = style.Foreground(lipgloss.Color("245"))
	}
	return style.Render(text)
}

// Labels retain their entire meaning on NO_COLOR/dumb terminals. Color is an
// accent, never the authority for an outcome; only validated metadata calls it.
func outcomeLabel(status string) string {
	color := lipgloss.Color("214")
	switch {
	case status == "succeeded":
		color = lipgloss.Color("78")
	case strings.HasPrefix(status, "failed"):
		color = lipgloss.Color("203")
	}
	return lipgloss.NewStyle().Foreground(color).Render("[" + status + "]")
}
