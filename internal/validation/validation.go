package validation

import (
	"regexp"
	"strings"
)

// RequiredLayers is the set of layers every VALIDATION.md declares: a layer
// that does not apply is declared as n/a with its reason, never omitted.
// Other names are allowed.
var RequiredLayers = []string{"unit", "integration", "contract", "e2e", "mutation"}

// layerNameRe is the accepted layer name: lowercase letters, digits and
// hyphens, at least one character.
var layerNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Layer is one declared test layer of the "## Test layers" section of a
// VALIDATION.md.
type Layer struct {
	// Name is the layer name as written (unit, integration, e2e, ...).
	Name string
	// Kind is one of "command" (a backticked command), "na" (n/a with a
	// reason) or "manual" (a description of what runs it).
	Kind string
	// Value is the command, the n/a reason, or the manual description —
	// the text the layer carries.
	Value string
	// Line is the 1-based number of the line that declared the layer.
	Line int
}

// Document is the parsed "## Test layers" section of a VALIDATION.md.
type Document struct {
	// HasLayers is true when the exact title "## Test layers" exists.
	HasLayers bool
	// Layers is the accepted layer lines, in file order.
	Layers []Layer
	// Malformed holds the 1-based line numbers of the list items in the
	// section that match no accepted form, in file order. Only the numbers
	// are kept: the line text never leaves the parser.
	Malformed []int
}

// sectionTitle is the exact title that opens the section.
const sectionTitle = "## Test layers"

// Parse analyses the text of a VALIDATION.md and returns the parsed
// document. Code fences are tracked from the first line of the file: a
// fenced title neither opens nor ends the section, and a fenced item is
// never a layer. The section runs from the exact (unfenced) title
// "## Test layers" to the next unfenced "## " title or the end of the
// file; outside the section, every line is ignored. Inside the section,
// non-list lines are ignored and each list item (a line starting with
// "- ") must be one of the three forms, else its line number lands in
// Document.Malformed. The parser reads nothing and writes nothing, and the
// line text never leaves it.
func Parse(text string) Document {
	doc := Document{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var fence fenceState
	start := -1
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if fence.consume(line) {
			continue
		}
		if line == sectionTitle {
			start = i
			break
		}
	}
	if start < 0 {
		return doc
	}
	declared := make(map[string]bool)
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if fence.consume(line) {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		layer, ok := parseLayerLine(line)
		if !ok || !layerNameRe.MatchString(layer.Name) {
			doc.Malformed = append(doc.Malformed, i+1)
			continue
		}
		if declared[layer.Name] {
			// A duplicated layer: the second occurrence is malformed; the
			// first declaration stands.
			doc.Malformed = append(doc.Malformed, i+1)
			continue
		}
		declared[layer.Name] = true
		layer.Line = i + 1
		doc.Layers = append(doc.Layers, layer)
	}
	doc.HasLayers = true
	return doc
}

// fenceState follows Markdown code fences. A fence opens with up to three
// spaces and three or more backticks or tildes, and closes with the same
// character, at least as many, and nothing else on the line.
type fenceState struct {
	char byte
	size int
}

// consume reports whether line belongs to a fence: a delimiter or a line
// inside one.
func (f *fenceState) consume(line string) bool {
	char, size, rest := fenceDelimiter(line)
	if f.char == 0 {
		if size == 0 || (char == '`' && strings.Contains(rest, "`")) {
			return false
		}
		f.char, f.size = char, size
		return true
	}
	if char == f.char && size >= f.size && strings.Trim(rest, " \t") == "" {
		f.char, f.size = 0, 0
	}
	return true
}

// fenceDelimiter returns the fence character, its count and what follows it,
// or a zero count when line is not a fence delimiter.
func fenceDelimiter(line string) (byte, int, string) {
	text := strings.TrimLeft(line, " ")
	if len(line)-len(text) > 3 || text == "" || (text[0] != '`' && text[0] != '~') {
		return 0, 0, ""
	}
	size := 0
	for size < len(text) && text[size] == text[0] {
		size++
	}
	if size < 3 {
		return 0, 0, ""
	}
	return text[0], size, text[size:]
}

// parseLayerLine parses one list item of the section. The name must match
// the layer grammar; the value must be one of the three forms. The command
// form is strict: the trimmed rest of the line is exactly one span between
// two backticks — no text before or after them, no backtick inside, no
// whitespace-only command — because the span is executed as a command.
func parseLayerLine(line string) (Layer, bool) {
	rest := line[2:]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return Layer{}, false
	}
	name := rest[:colon]
	value := strings.TrimSpace(rest[colon+1:])
	if value == "" {
		return Layer{}, false
	}
	switch {
	case strings.HasPrefix(value, "n/a:"):
		reason := strings.TrimSpace(value[len("n/a:"):])
		if reason == "" {
			return Layer{}, false
		}
		return Layer{Name: name, Kind: "na", Value: reason}, true
	case strings.HasPrefix(value, "manual:"):
		desc := strings.TrimSpace(value[len("manual:"):])
		if desc == "" {
			return Layer{}, false
		}
		return Layer{Name: name, Kind: "manual", Value: desc}, true
	case strings.HasPrefix(value, "`"):
		if len(value) < 2 || !strings.HasSuffix(value, "`") {
			return Layer{}, false
		}
		inner := value[1 : len(value)-1]
		if strings.ContainsRune(inner, '`') {
			return Layer{}, false
		}
		command := strings.TrimSpace(inner)
		if command == "" {
			return Layer{}, false
		}
		return Layer{Name: name, Kind: "command", Value: command}, true
	default:
		return Layer{}, false
	}
}

// IsPlaceholder reports whether the layer value still carries the template
// placeholder: it is a template line until a real value replaces it.
func IsPlaceholder(value string) bool {
	return strings.Contains(value, "<command>") ||
		strings.Contains(value, "<reason>") ||
		strings.Contains(value, "<what runs it>")
}
