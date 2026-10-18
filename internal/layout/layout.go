// Package layout contains the deterministic pane placement rules.
package layout

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

type Grid struct {
	Cols int   `json:"cols"`
	Rows []int `json:"rows_per_col"`
}

func GridSizes(n int) Grid {
	if n < 1 {
		return Grid{Rows: []int{}}
	}
	if n == 7 {
		// Seven panes: the caller (the orchestrator) alone in its column, 1+3+3,
		// not the 2+2+3 the square split gives (user decision, 2026-09-29).
		return Grid{Cols: 3, Rows: []int{1, 3, 3}}
	}
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	base, extra := n/cols, n%cols
	rows := make([]int, cols)
	for c := range rows {
		rows[c] = base
		if c >= cols-extra {
			rows[c]++
		}
	}
	return Grid{Cols: cols, Rows: rows}
}

type Anchor struct {
	Anchor    string `json:"anchor,omitempty"`
	Direction string `json:"direction,omitempty"`
	Overflow  string `json:"overflow,omitempty"`
}

func layoutField(value any, key string) any {
	object, ok := value.(*jsonjs.Object)
	if !ok {
		return jsonjs.Undefined
	}
	field, exists := object.Get(key)
	if !exists {
		return jsonjs.Undefined
	}
	return field
}

func jsNumber(value any) float64 {
	if jsonjs.IsUndefined(value) {
		return math.NaN()
	}
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		if strings.TrimSpace(v) == "" {
			return 0
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return math.NaN()
		}
		return n
	default:
		return math.NaN()
	}
}

// LayoutArea returns the usable pane-layout area, inferring it from pane rectangles when needed.
func LayoutArea(raw []byte) (float64, float64, bool) {
	doc, err := jsonjs.Parse(raw)
	if err != nil {
		return 0, 0, false
	}
	l := layoutField(layoutField(layoutField(doc, "result"), "layout"), "area")
	w, h := jsNumber(layoutField(l, "width")), jsNumber(layoutField(l, "height"))
	if !math.IsNaN(w) && !math.IsInf(w, 0) && w > 0 && !math.IsNaN(h) && !math.IsInf(h, 0) && h > 0 {
		return w, h, true
	}
	panes, ok := layoutField(layoutField(layoutField(doc, "result"), "layout"), "panes").([]any)
	if !ok || len(panes) == 0 {
		return 0, 0, false
	}
	w, h = 0, 0
	for _, pane := range panes {
		r := layoutField(pane, "rect")
		x, pw := jsNumber(layoutField(r, "x")), jsNumber(layoutField(r, "width"))
		y, ph := jsNumber(layoutField(r, "y")), jsNumber(layoutField(r, "height"))
		if !math.IsNaN(x) && !math.IsInf(x, 0) && !math.IsNaN(pw) && !math.IsInf(pw, 0) {
			w = math.Max(w, x+pw)
		}
		if !math.IsNaN(y) && !math.IsInf(y, 0) && !math.IsNaN(ph) && !math.IsInf(ph, 0) {
			h = math.Max(h, y+ph)
		}
	}
	return w, h, w > 0 && h > 0
}

type LayoutFraction struct {
	PaneID string
	Width  float64
	Height float64
}

// LayoutFractions returns finite pane-size fractions in layout order.
func LayoutFractions(raw []byte) []LayoutFraction {
	doc, err := jsonjs.Parse(raw)
	if err != nil {
		return nil
	}
	areaW, areaH, ok := LayoutArea(raw)
	if !ok {
		return nil
	}
	panes, ok := layoutField(layoutField(layoutField(doc, "result"), "layout"), "panes").([]any)
	if !ok {
		return nil
	}
	fractions := []LayoutFraction{}
	for _, pane := range panes {
		id, ok := layoutField(pane, "pane_id").(string)
		if !ok {
			continue
		}
		r := layoutField(pane, "rect")
		w := jsNumber(layoutField(r, "width")) / areaW
		h := jsNumber(layoutField(r, "height")) / areaH
		if math.IsNaN(w) || math.IsInf(w, 0) || math.IsNaN(h) || math.IsInf(h, 0) {
			continue
		}
		fractions = append(fractions, LayoutFraction{PaneID: id, Width: w, Height: h})
	}
	return fractions
}

// SplitAnchor returns nil when the document contains no usable candidate.
func SplitAnchor(raw []byte, me string, mine []string, cap int, min float64) *Anchor {
	doc, err := jsonjs.Parse(raw)
	if err != nil {
		return nil
	}
	areaW, areaH, usable := LayoutArea(raw)
	if !usable {
		return nil
	}
	panes, ok := layoutField(layoutField(layoutField(doc, "result"), "layout"), "panes").([]any)
	if !ok {
		return nil
	}
	type candidate struct {
		id               string
		me               bool
		x, y, w, h, area float64
	}
	var cs []candidate
	for _, p := range panes {
		paneID, ok := layoutField(p, "pane_id").(string)
		if !ok {
			continue
		}
		isMine := paneID == me
		if !isMine {
			for _, mineID := range mine {
				if paneID == mineID {
					isMine = true
					break
				}
			}
		}
		if !isMine {
			continue
		}
		r := layoutField(p, "rect")
		x, y := jsNumber(layoutField(r, "x")), jsNumber(layoutField(r, "y"))
		wRaw, hRaw := jsNumber(layoutField(r, "width")), jsNumber(layoutField(r, "height"))
		w, h := math.Round(wRaw/areaW*100)/100, math.Round(hRaw/areaH*100)/100
		if math.IsNaN(w) || math.IsInf(w, 0) || math.IsNaN(h) || math.IsInf(h, 0) {
			continue
		}
		if math.IsNaN(x) || x == 0 {
			x = 0
		}
		if math.IsNaN(y) || y == 0 {
			y = 0
		}
		cs = append(cs, candidate{paneID, paneID == me, x, y, w, h, w * h})
	}
	if len(cs) == 0 {
		return nil
	}
	if len(cs) >= cap {
		return &Anchor{Overflow: "full"}
	}
	var best *candidate
	for i := range cs {
		c := &cs[i]
		if math.Max(c.w, c.h)/2 < min {
			continue
		}
		if best == nil || c.area > best.area || c.area == best.area && (best.me && !c.me || best.me == c.me && (c.y < best.y || c.y == best.y && c.x < best.x)) {
			best = c
		}
	}
	if best == nil {
		return &Anchor{Overflow: "min"}
	}
	direction := "down"
	if best.w >= best.h {
		direction = "right"
	}
	return &Anchor{Anchor: best.id, Direction: direction}
}

func SplitCap(ctx *core.Config, env platform.Env) int {
	if core.LanesEnabled(ctx, env) && !core.ConfigExplicit(ctx, "split_max_panes", env) {
		mw, _ := strconv.Atoi(core.MaxWorkers(ctx, env))
		if mw > 0 {
			return 1 + mw
		}
		panes, _ := strconv.Atoi(core.PanesValue(ctx, env))
		if core.PaneMode(ctx, env) == "flex" {
			return panes + core.FlexExtra(ctx, env)
		}
		return panes
	}
	v := core.Cfg(ctx, "split_max_panes", "4", env)
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(v) {
		return 4
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 4
	}
	return n
}

func SplitMin(ctx *core.Config, env platform.Env) float64 {
	v := core.Cfg(ctx, "split_min_pane", "0.18", env)
	if !regexp.MustCompile(`^0?\.[0-9]+$`).MatchString(v) {
		return .18
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return .18
	}
	return n
}

func AutoDirectionFor(env platform.Env, pane string) string {
	r := herdr.PaneLayout(env, pane)
	if r.Stdout == "" {
		return "right"
	}
	var doc struct {
		Result struct {
			Layout struct {
				Panes []struct {
					ID      string `json:"pane_id"`
					Focused bool   `json:"focused"`
					Rect    struct {
						Width  float64 `json:"width"`
						Height float64 `json:"height"`
					} `json:"rect"`
				} `json:"panes"`
			} `json:"layout"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(r.Stdout), &doc) != nil || doc.Result.Layout.Panes == nil {
		return "down"
	}
	sel := pane
	if sel == "" {
		sel = env.Get("HERDR_PANE_ID")
	}
	for _, p := range doc.Result.Layout.Panes {
		if p.ID == sel || (sel == "" && p.Focused) {
			if p.Rect.Width >= 160 && p.Rect.Width >= 2*p.Rect.Height {
				return "right"
			}
			return "down"
		}
	}
	return "down"
}

func UIFocusedPane(env platform.Env) string {
	for _, value := range herdr.PaneListAll(env) {
		p, ok := value.(*jsonjs.Object)
		if !ok {
			continue
		}
		focused, _ := p.Get("focused")
		if focused != true {
			continue
		}
		id, _ := p.Get("pane_id")
		if id == nil {
			return ""
		}
		return fmt.Sprint(id)
	}
	return ""
}

func RestoreFocusIfStolen(prev, stolen, direction string, env platform.Env) {
	if prev == "" || stolen == "" || prev == stolen || UIFocusedPane(env) != stolen {
		return
	}
	if herdr.AgentFocus(prev, env) || prev != env.Get("HERDR_PANE_ID") {
		return
	}
	back := ""
	if direction == "right" {
		back = "left"
	} else if direction == "down" {
		back = "up"
	}
	if back != "" {
		herdr.PaneFocusBack(back, stolen, env)
	}
}

func PickSplitAnchor(ctx *core.Config, env platform.Env, cwd string) *Anchor {
	me := env.Get("HERDR_PANE_ID")
	r := herdr.PaneLayout(env, "")
	if r.Stdout == "" {
		return &Anchor{Anchor: me, Direction: "right"}
	}
	var doc any
	if json.Unmarshal([]byte(r.Stdout), &doc) != nil {
		return &Anchor{Anchor: me, Direction: "right"}
	}
	dir := core.StateDirPath(ctx, env, cwd)
	mine := []string{}
	for _, row := range core.RosterRows(dir) {
		fields := strings.Split(row, "\t")
		if len(fields) > 1 && fields[1] != "" {
			mine = append(mine, fields[1])
		}
	}
	var out *Anchor
	if cols, overflow, declared := PaneColumns(ctx, env); declared {
		cap, unlimited := SplitCapDeclared(ctx, env)
		out = SplitAnchorDeclared([]byte(r.Stdout), me, mine, cols, overflow, cap, unlimited, SplitMin(ctx, env))
	} else {
		out = SplitAnchor([]byte(r.Stdout), me, mine, SplitCap(ctx, env), SplitMin(ctx, env))
	}
	if out == nil {
		return &Anchor{Anchor: me, Direction: "right"}
	}
	return out
}

func layoutJSON(value any) string { return jsonjs.Stringify(value) }
