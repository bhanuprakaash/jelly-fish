package provider

import "strings"

const (
	titlePrefix = "Write a 3–6 word title for a chat that starts with: "
	titleSuffix = ". Reply with the title only."
)

// TitlePrompt asks for the Title of a chat whose first message is message
// (event-log.md D45).
func TitlePrompt(message string) string {
	return titlePrefix + message + titleSuffix
}

// TitleMessage is the inverse of TitlePrompt: it returns the first message a
// title request carries, or false if prompt is not one.
func TitleMessage(prompt string) (string, bool) {
	rest, ok := strings.CutPrefix(prompt, titlePrefix)
	if !ok {
		return "", false
	}
	return strings.CutSuffix(rest, titleSuffix)
}
