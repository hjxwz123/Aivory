package llm

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// titleMathContentToPlainText mirrors the composer's math-title projection:
// valid math delimiters are removed while their LaTeX remains readable. Code
// spans, fenced/indented code, currency-like dollars, and malformed delimiters
// stay literal.
func titleMathContentToPlainText(source string) string {
	var plain strings.Builder
	plain.Grow(len(source))
	textStart := 0
	index := 0
	inlineSlashCloseExhausted := false
	blockSlashCloseExhausted := false

	flushText := func(end int) {
		plain.WriteString(source[textStart:end])
	}

	for index < len(source) {
		if titleIsIndentedCodeLine(source, index) {
			lineEnd := strings.IndexByte(source[index:], '\n')
			if lineEnd < 0 {
				index = len(source)
			} else {
				index += lineEnd + 1
			}
			continue
		}

		if source[index] == '~' && titleIsFenceMarker(source, index, '~') {
			run := titleMarkerRunLength(source, index, '~')
			delimiter := strings.Repeat("~", run)
			if closeOffset := strings.Index(source[index+run:], delimiter); closeOffset >= 0 {
				index = index + run + closeOffset + run
				continue
			}
		}

		if source[index] == '`' && !titleIsEscaped(source, index) {
			run := titleMarkerRunLength(source, index, '`')
			delimiter := strings.Repeat("`", run)
			if closeOffset := strings.Index(source[index+run:], delimiter); closeOffset >= 0 {
				index = index + run + closeOffset + run
				continue
			}
		}

		var slashClose string
		var closeExhausted *bool
		if strings.HasPrefix(source[index:], `\(`) && !titleIsEscaped(source, index) {
			slashClose = `\)`
			closeExhausted = &inlineSlashCloseExhausted
		} else if strings.HasPrefix(source[index:], `\[`) && !titleIsEscaped(source, index) {
			slashClose = `\]`
			closeExhausted = &blockSlashCloseExhausted
		}

		if slashClose != "" {
			if *closeExhausted {
				index += 2
				continue
			}
			close := titleFindUnescaped(source, slashClose, index+2)
			if close < 0 {
				*closeExhausted = true
			} else {
				opener := source[index : index+2]
				innermostOpen := index
				for nestedOpen := titleFindUnescaped(source, opener, innermostOpen+2); nestedOpen >= 0 && nestedOpen < close; nestedOpen = titleFindUnescaped(source, opener, innermostOpen+2) {
					innermostOpen = nestedOpen
				}
				if innermostOpen != index {
					index = innermostOpen
					continue
				}
				value := strings.TrimSpace(source[index+2 : close])
				if value != "" {
					flushText(index)
					plain.WriteString(value)
					index = close + 2
					textStart = index
					continue
				}
			}
		}

		if strings.HasPrefix(source[index:], "$$") && !titleIsEscaped(source, index) {
			close := titleFindUnescaped(source, "$$", index+2)
			if close >= 0 {
				value := strings.TrimSpace(source[index+2 : close])
				if value != "" {
					flushText(index)
					plain.WriteString(value)
					index = close + 2
					textStart = index
					continue
				}
			}
		}

		if source[index] == '$' && !titleIsEscaped(source, index) && (index+1 >= len(source) || source[index+1] != '$') {
			previous := titleRuneBefore(source, index)
			next, nextWidth := utf8.DecodeRuneInString(source[index+1:])
			if !titleIsWordCharacter(previous) && nextWidth > 0 && !unicode.IsSpace(next) && !unicode.IsDigit(next) {
				close := index + 1
				foundClose := false
				for close < len(source) {
					offset := strings.IndexByte(source[close:], '$')
					if offset < 0 {
						break
					}
					close += offset
					if !titleIsEscaped(source, close) && (close+1 >= len(source) || source[close+1] != '$') {
						foundClose = true
						break
					}
					close++
				}
				if foundClose {
					beforeClose := titleRuneBefore(source, close)
					afterClose, _ := utf8.DecodeRuneInString(source[close+1:])
					value := strings.TrimSpace(source[index+1 : close])
					if value != "" && !unicode.IsSpace(beforeClose) && !titleIsWordCharacter(afterClose) {
						flushText(index)
						plain.WriteString(value)
						index = close + 1
						textStart = index
						continue
					}
				}
			}
		}

		_, width := utf8.DecodeRuneInString(source[index:])
		if width == 0 {
			break
		}
		index += width
	}

	flushText(len(source))
	return plain.String()
}

func titleIsEscaped(source string, index int) bool {
	slashCount := 0
	for i := index - 1; i >= 0 && source[i] == '\\'; i-- {
		slashCount++
	}
	return slashCount%2 == 1
}

func titleFindUnescaped(source, needle string, from int) int {
	for from <= len(source) {
		offset := strings.Index(source[from:], needle)
		if offset < 0 {
			return -1
		}
		index := from + offset
		if !titleIsEscaped(source, index) {
			return index
		}
		from = index + len(needle)
	}
	return -1
}

func titleMarkerRunLength(source string, start int, marker byte) int {
	length := 0
	for start+length < len(source) && source[start+length] == marker {
		length++
	}
	return length
}

func titleIsIndentedCodeLine(source string, index int) bool {
	if index > 0 && source[index-1] != '\n' {
		return false
	}
	return source[index] == '\t' || strings.HasPrefix(source[index:], "    ")
}

func titleIsFenceMarker(source string, index int, marker byte) bool {
	lineStart := strings.LastIndexByte(source[:index], '\n') + 1
	prefix := source[lineStart:index]
	return len(prefix) <= 3 && strings.Trim(prefix, " ") == "" && titleMarkerRunLength(source, index, marker) >= 3
}

func titleRuneBefore(source string, index int) rune {
	if index <= 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(source[:index])
	return r
}

func titleIsWordCharacter(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsNumber(value)
}
