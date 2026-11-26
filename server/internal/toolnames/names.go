// Package toolnames owns the stable names of Aivory's local Function tools.
// Provider-hosted tools use their provider-defined names and must not be
// aliased into this namespace.
package toolnames

const (
	AivoryWebSearch       = "aivory_web_search"
	LegacyAivoryWebSearch = "web_search"
	DocList               = "doc_list"
	DocGrep               = "doc_grep"
	DocRead               = "doc_read"
)

// IsDocumentTool reports whether name is one of the internal Grep-mode document
// inspection tools injected dynamically when documents are in scope.
func IsDocumentTool(name string) bool {
	switch name {
	case DocList, DocGrep, DocRead:
		return true
	default:
		return false
	}
}
