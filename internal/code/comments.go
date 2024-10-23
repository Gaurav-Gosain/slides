package code

import (
	"regexp"
	"strings"
)

const comment = "///"

var (
	commentRegexp = regexp.MustCompile("(?m)[\r\n]+^" + comment + ".*$")
	// ```(?:\s*\w+)?\s*\n\s*```
	emptyCodeBlockRegexp = regexp.MustCompile("(?m)^```(?:\\s*\\w+)?\\s*\\n\\s*```$")
)

// HideComments removes all comments from the given content.
func HideComments(content string) string {
	cleanedContent := commentRegexp.ReplaceAllString(content, "")
	// Remove empty code blocks if ``` is the only thing in the code block
	return emptyCodeBlockRegexp.ReplaceAllString(cleanedContent, "")
}

// RemoveComments strips all the comments from the given content.
// This is useful for when we want to actually use the content of the comments.
func RemoveComments(content string) string {
	return strings.ReplaceAll(content, comment, "")
}
