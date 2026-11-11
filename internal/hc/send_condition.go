package hc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// GuardIdle and GuardReady are the --if guard words a guarded ordinary
// (non-queue) send may select, proven per call — never assumed, never cached.
const (
	GuardIdle  = "idle"
	GuardReady = "ready"
)

// SendIfGuard selects the --if guard word for one ordinary (non-queue) hc
// send: the parser values are the --if possible-values section of `hc send
// --help` (the local CLI rejects an unknown word at parse time, before any
// RPC), and the serving enum is the terminal.send if param of the OpenRPC
// document of the core serving the terminal — `hc schema` for machine "",
// else one `hc schema --on <machine>`, only after the local CLI proves the
// --on option and the local rpc.discover proves its on param, so a stale
// local core cannot answer the remote query with its own enum. GuardReady
// only when both prove it, GuardIdle only when both prove it, an error
// otherwise (help or schema error, missing or ambiguous --if, malformed,
// multi-document or trailing-JSON document, unresolved or external
// reference, no proven common guard): the caller's zero send under the one
// shared ctx deadline. This method issues only the read-only probes.
func (c *Client) SendIfGuard(ctx context.Context, machine string) (string, error) {
	parser, err := c.sendIfParser(ctx)
	if err != nil {
		return "", err
	}
	var serving []string
	if machine == "" {
		serving, err = c.servingIfEnum(ctx)
	} else {
		serving, err = c.remoteIfEnum(ctx, machine)
	}
	if err != nil {
		return "", err
	}
	return chooseGuard(parser, serving)
}

func (c *Client) sendIfParser(ctx context.Context) ([]string, error) {
	out, err := decode(c.Launch(ctx, c.Env, "send", "--help"))
	if err != nil {
		return nil, fmt.Errorf("hc send --help: %w", err)
	}
	return sendIfHelpValues(out)
}

func (c *Client) servingIfEnum(ctx context.Context) ([]string, error) {
	out, err := c.Run(ctx, "schema")
	if err != nil {
		return nil, fmt.Errorf("hc schema: %w", err)
	}
	return schemaIfEnum(out)
}

// remoteIfEnum proves one machine's serving enum: after the local preflight
// (the --on option, the rpc.discover on param) one `hc schema --on
// <machine>`, parsed by the same rule as the local one.
func (c *Client) remoteIfEnum(ctx context.Context, machine string) ([]string, error) {
	out, err := decode(c.Launch(ctx, c.Env, "schema", "--help"))
	if err != nil {
		return nil, fmt.Errorf("hc schema --help: %w", err)
	}
	if err := schemaOnHelp(out); err != nil {
		return nil, fmt.Errorf("hc schema --help: %w", err)
	}
	out, err = c.Run(ctx, "schema")
	if err != nil {
		return nil, fmt.Errorf("hc schema: %w", err)
	}
	doc, err := schemaDocument(out)
	if err != nil {
		return nil, err
	}
	if err := discoverExposesOn(doc); err != nil {
		return nil, err
	}
	out, err = c.Run(ctx, "schema", "--on", machine)
	if err != nil {
		return nil, fmt.Errorf("hc schema --on %s: %w", machine, err)
	}
	return schemaIfEnum(out)
}

// discoverExposesOn proves the local serving rpc.discover exposes its on param.
func discoverExposesOn(doc *jsonjs.Object) error {
	method, err := uniqueNamed(doc, "methods", "method", "rpc.discover")
	if err != nil {
		return err
	}
	if _, err := uniqueNamed(method, "params", "param", "on"); err != nil {
		return fmt.Errorf("local rpc.discover: %w", err)
	}
	return nil
}

// optionLines returns the help lines whose first field is the option: exactly
// one, or an error naming the absence or the duplication (never a guess).
func optionLines(help, option string) ([]string, error) {
	var lines []string
	for _, line := range strings.Split(help, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == option {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("the help has no %s option line", option)
	}
	if len(lines) > 1 {
		return nil, fmt.Errorf("the help names the %s option more than once", option)
	}
	return lines, nil
}

// sendIfHelpValues reads the [possible values: ...] section of the --if line
// as comma-separated words, each nonempty after trimming.
func sendIfHelpValues(help string) ([]string, error) {
	lines, err := optionLines(help, "--if")
	if err != nil {
		return nil, err
	}
	line := lines[0]
	start := strings.Index(line, "[possible values:")
	if start < 0 {
		return nil, errors.New("the --if help line carries no [possible values] section")
	}
	rest := line[start+len("[possible values:"):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return nil, errors.New("the --if possible-values section is not closed")
	}
	values := make([]string, 0, 1)
	for _, part := range strings.Split(rest[:end], ",") {
		word := strings.TrimSpace(part)
		if word == "" {
			return nil, errors.New("the --if possible-values section holds an empty word")
		}
		values = append(values, word)
	}
	return values, nil
}

// schemaOnHelp proves the --on option taking a value in `hc schema --help`
// (the planned --on interface; without it the remote query is refused).
func schemaOnHelp(help string) error {
	lines, err := optionLines(help, "--on")
	if err != nil {
		return err
	}
	for _, field := range strings.Fields(lines[0])[1:] {
		if strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">") {
			return nil
		}
	}
	return errors.New("the schema --on option takes no value")
}

// schemaIfEnum reads the terminal.send if enum out of one schema answer.
func schemaIfEnum(stdout string) ([]string, error) {
	doc, err := schemaDocument(stdout)
	if err != nil {
		return nil, err
	}
	method, err := uniqueNamed(doc, "methods", "method", "terminal.send")
	if err != nil {
		return nil, err
	}
	param, err := uniqueNamed(method, "params", "param", "if")
	if err != nil {
		return nil, fmt.Errorf("terminal.send: %w", err)
	}
	raw, ok := param.Get("schema")
	if !ok || raw == nil {
		return nil, errors.New("terminal.send if param carries no schema")
	}
	schema, ok := raw.(*jsonjs.Object)
	if !ok {
		return nil, errors.New("terminal.send if param schema is not an object")
	}
	return resolveIfEnum(doc, schema)
}

// schemaDocument decodes one schema answer as the single complete JSON
// object document it must be (jsonjs.Parse refuses a second document or
// trailing data).
func schemaDocument(stdout string) (*jsonjs.Object, error) {
	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("the schema is not one complete JSON document: %v", err)
	}
	doc, ok := v.(*jsonjs.Object)
	if !ok {
		return nil, errors.New("the schema document is not a JSON object")
	}
	return doc, nil
}

// uniqueNamed finds the object named name in owner's listKey array: exactly
// once — a missing or duplicated entry is malformed metadata, never a guess.
func uniqueNamed(owner *jsonjs.Object, listKey, itemLabel, name string) (*jsonjs.Object, error) {
	raw, ok := owner.Get(listKey)
	if !ok {
		return nil, fmt.Errorf("no %s list", listKey)
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the %s list is not an array", listKey)
	}
	var found *jsonjs.Object
	count := 0
	for _, item := range list {
		entry, ok := item.(*jsonjs.Object)
		if !ok {
			return nil, fmt.Errorf("the %s list holds a non-object %s", listKey, itemLabel)
		}
		if v, ok := entry.Get("name"); !ok || v != name {
			continue
		}
		found = entry
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("no %s named %s", itemLabel, name)
	}
	if count > 1 {
		return nil, fmt.Errorf("more than one %s named %s", itemLabel, name)
	}
	return found, nil
}

// resolveIfEnum reads the guard enum out of the if param's JSON schema: a
// direct string enum, or a nullable union of one local components reference
// and one null; anything else is an error, never a guess.
func resolveIfEnum(doc *jsonjs.Object, schema *jsonjs.Object) ([]string, error) {
	if enum, ok := stringEnum(schema); ok {
		return enum, nil
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		raw, ok := schema.Get(key)
		if !ok {
			continue
		}
		branches, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("the if schema %s is not an array", key)
		}
		refName, refs, nulls := "", 0, 0
		for _, item := range branches {
			branch, ok := item.(*jsonjs.Object)
			if !ok {
				return nil, fmt.Errorf("the if schema %s holds a non-object branch", key)
			}
			if v, ok := branch.Get("$ref"); ok {
				name, err := localRefName(v)
				if err != nil {
					return nil, err
				}
				if refs++; refs > 1 {
					return nil, fmt.Errorf("the if schema %s names more than one $ref", key)
				}
				refName = name
				continue
			}
			if t, ok := branch.Get("type"); ok && t == "null" {
				nulls++
				continue
			}
			return nil, fmt.Errorf("the if schema %s holds an unexpected branch", key)
		}
		if refs != 1 || nulls != 1 {
			return nil, fmt.Errorf("the if schema %s is not one local reference and one null", key)
		}
		return resolveRefChain(doc, refName)
	}
	return nil, errors.New("the if schema carries no enum or a nullable local reference")
}

// resolveRefChain walks the named component and every local $ref it follows
// to its enum, with a visited set: a revisited component is an unbounded
// cycle.
func resolveRefChain(doc *jsonjs.Object, name string) ([]string, error) {
	visited := map[string]bool{name: true}
	target, err := componentNamed(doc, name)
	if err != nil {
		return nil, err
	}
	for {
		if enum, ok := stringEnum(target); ok {
			return enum, nil
		}
		raw, ok := target.Get("$ref")
		if !ok {
			return nil, errors.New("the if schema $ref chain ends without an enum")
		}
		next, err := localRefName(raw)
		if err != nil {
			return nil, err
		}
		if visited[next] {
			return nil, fmt.Errorf("the if schema $ref chain revisits components.schemas.%s", next)
		}
		visited[next] = true
		target, err = componentNamed(doc, next)
		if err != nil {
			return nil, err
		}
	}
}

// localRefName names the component one $ref refers to: a flat local
// #/components/schemas/<name> reference; an external URI, a nested path, or
// a non-string is refused (it can never be resolved from one document).
func localRefName(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", errors.New("the if schema $ref is not a string")
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("the if schema $ref %s is not a local components reference", s)
	}
	name := s[len(prefix):]
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("the if schema $ref %s is not a flat component name", s)
	}
	return name, nil
}

// componentNamed reads components.schemas.<name> as the object it must be:
// null, a scalar, or an array at the components or the schemas level is a
// controlled error, never a type-assertion panic.
func componentNamed(doc *jsonjs.Object, name string) (*jsonjs.Object, error) {
	comps, ok := doc.Get("components")
	if !ok {
		return nil, errors.New("the schema has no components")
	}
	compsObj, ok := comps.(*jsonjs.Object)
	if !ok {
		return nil, errors.New("the schema components are not an object")
	}
	schemas, ok := compsObj.Get("schemas")
	if !ok {
		return nil, errors.New("the schema components hold no schemas")
	}
	schemasObj, ok := schemas.(*jsonjs.Object)
	if !ok {
		return nil, errors.New("the schema components.schemas is not an object")
	}
	raw, ok := schemasObj.Get(name)
	if !ok {
		return nil, fmt.Errorf("the schema has no components.schemas.%s", name)
	}
	obj, ok := raw.(*jsonjs.Object)
	if !ok {
		return nil, fmt.Errorf("components.schemas.%s is not an object", name)
	}
	return obj, nil
}

func stringEnum(schema *jsonjs.Object) ([]string, bool) {
	raw, ok := schema.Get("enum")
	if !ok {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// chooseGuard picks ready only when both sets prove it, idle only when both
// prove it, and the refusal — the caller's zero send — otherwise.
func chooseGuard(parser, serving []string) (string, error) {
	proves := func(set []string, word string) bool {
		for _, w := range set {
			if w == word {
				return true
			}
		}
		return false
	}
	if proves(parser, GuardReady) && proves(serving, GuardReady) {
		return GuardReady, nil
	}
	if proves(parser, GuardIdle) && proves(serving, GuardIdle) {
		return GuardIdle, nil
	}
	return "", fmt.Errorf("no --if guard is proven on both sides: the parser holds %v, the serving enum holds %v", parser, serving)
}
