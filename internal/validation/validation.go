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
	// Note is optional explanatory text following a command.
	Note string
	// Line is the 1-based number of the line that declared the layer.
	Line int
}

// Document is the parsed "## Test layers" section of a VALIDATION.md.
type Document struct {
	// HasLayers is true when a matching title or a section with layer items exists.
	HasLayers bool
	// SectionLine is the 1-based heading line; HasTable marks a table in it.
	SectionLine int
	HasTable    bool
	// Layers is the accepted layer lines, in file order.
	Layers []Layer
	// Malformed holds the 1-based line numbers of the list items in the
	// section that match no accepted form, in file order. Only the numbers
	// are kept: the line text never leaves the parser.
	Malformed []int
}

// isSectionTitle recognizes the English title with an optional numeric prefix,
// and preserves the previously supported Portuguese title.
func isSectionTitle(line string) bool {
	if !strings.HasPrefix(line, "## ") {
		return false
	}
	title := strings.TrimPrefix(line, "## ")
	i := 0
	for i < len(title) && title[i] >= '0' && title[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(title) && (title[i] == '.' || title[i] == ')') && title[i+1] == ' ' {
		title = title[i+2:]
	}
	return asciiEqualFold(title, "Test layers") || asciiEqualFold(title, "Camadas de teste")
}

// asciiEqualFold compares a and b ignoring the case of ASCII letters only.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Parse finds an unfenced matching heading or the first level-2 section with
// a valid required layer declaration, then parses items up to the next level-2 heading.
// Fenced headings and items are ignored.
func Parse(text string) Document {
	doc := Document{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var fence fenceState
	start, candidate, fallback := -1, -1, -1
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if fence.consume(line) {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			candidate = i
			if isSectionTitle(line) {
				start = i
				break
			}
			continue
		}
		if candidate >= 0 && fallback < 0 && strings.HasPrefix(line, "- ") {
			layer, ok := parseLayerLine(line)
			if ok {
				for _, name := range RequiredLayers {
					if layer.Name == name {
						fallback = candidate
						break
					}
				}
			}
		}
	}
	if start < 0 {
		start = fallback
	}
	if start < 0 {
		return doc
	}
	doc.SectionLine = start + 1
	fence = fenceState{}
	declared := make(map[string]bool)
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if fence.consume(line) {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			doc.HasTable = true
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
// the layer grammar; the value must be one of the four forms. A command is
// one nonempty backticked span, optionally followed by a separated note.
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
	case strings.HasPrefix(value, "covered:"):
		target := strings.TrimSpace(strings.TrimPrefix(value, "covered:"))
		note := ""
		if i := strings.IndexAny(target, "—–"); i >= 0 {
			if i == 0 || target[i-1] != ' ' || len(target) <= i+len("—") || target[i+len("—")] != ' ' {
				return Layer{}, false
			}
			note = strings.TrimSpace(target[i+len("—"):])
			target = strings.TrimSpace(target[:i])
			if note == "" {
				return Layer{}, false
			}
		}
		if !layerNameRe.MatchString(target) {
			return Layer{}, false
		}
		return Layer{Name: name, Kind: "covered", Value: target, Note: note}, true
	case strings.HasPrefix(value, "`"):
		end := strings.IndexByte(value[1:], '`')
		if end < 0 {
			return Layer{}, false
		}
		end++
		inner := value[1:end]
		tail := value[end+1:]
		note := ""
		if tail != "" {
			if !strings.HasPrefix(tail, " — ") && !strings.HasPrefix(tail, " – ") {
				return Layer{}, false
			}
			note = strings.TrimSpace(tail[len(" — "):])
			if note == "" {
				return Layer{}, false
			}
		}
		command := strings.TrimSpace(inner)
		if command == "" {
			return Layer{}, false
		}
		return Layer{Name: name, Kind: "command", Value: command, Note: note}, true
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
