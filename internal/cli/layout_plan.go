package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/layout"
	"github.com/djalmajr/soho/internal/platform"
)

type layoutCandidate struct {
	PaneID string  `json:"pane_id"`
	Caller bool    `json:"caller"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type layoutPlan struct {
	Placement  string            `json:"placement"`
	Anchor     *string           `json:"anchor"`
	Direction  *string           `json:"direction"`
	Reason     string            `json:"reason"`
	Cap        int               `json:"cap"`
	MinPane    float64           `json:"min_pane"`
	Candidates []layoutCandidate `json:"candidates"`
	Grid       any               `json:"grid"`
}

// splitCap and splitMin are the spawn's own values, so layout-plan shows where
// the next spawn really lands.
func splitCap(ctx *core.Config, env platform.Env) int { return layout.SplitCap(ctx, env) }

func splitMin(ctx *core.Config, env platform.Env) float64 { return layout.SplitMin(ctx, env) }

func cmdLayoutPlan(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	file, me, mine := "", env.Get("HERDR_PANE_ID"), ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "--layout" && a != "--me" && a != "--mine" {
			platform.Die("layout-plan: unknown option "+a, 2)
		}
		if i+1 >= len(args) {
			platform.Die("layout-plan: "+a+" expects a value", 2)
		}
		i++
		switch a {
		case "--layout":
			file = args[i]
		case "--me":
			me = args[i]
		case "--mine":
			mine = args[i]
		}
	}
	var raw []byte
	if file != "" {
		var err error
		if file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(file)
		}
		if err != nil {
			message := ""
			switch {
			case os.IsNotExist(err):
				message = "No such file or directory"
			case os.IsPermission(err):
				message = "Permission denied"
			default:
				if info, statErr := os.Stat(file); statErr == nil && info.IsDir() {
					message = "Is a directory"
				} else if pathErr, ok := err.(*os.PathError); ok {
					message = pathErr.Err.Error()
				} else {
					message = err.Error()
				}
			}
			_, _ = fmt.Fprintf(platform.Stderr, "cat: %s: %s\n", file, message)
			platform.Die(fmt.Sprintf("layout-plan: cannot read %s", file), 2)
		}
	} else {
		herdr.RequireEnv(env, platform.Current(), os.Getpid(), nil)
		r := herdr.PaneLayout(env, "")
		if r.NotFound || r.Status == nil || *r.Status != 0 {
			platform.Die("pane layout failed", 4)
		}
		raw = []byte(r.Stdout)
		if mine == "" {
			dir := core.StateDirPath(ctx, env, cwd)
			for _, row := range core.RosterRows(dir) {
				fields := strings.Split(row, "\t")
				if len(fields) > 1 && fields[1] != "" {
					mine += " " + fields[1]
				}
			}
		}
	}
	if !json.Valid(raw) {
		if file != "" {
			label := file
			if label == "-" {
				label = "stdin"
			}
			platform.Die("layout-plan: "+label+" is not a pane layout JSON document", 2)
		}
		platform.Die("pane layout failed", 4)
	}
	ids := strings.Fields(mine)
	min := splitMin(ctx, env)
	cols, overflow, declared := layout.PaneColumns(ctx, env)
	cap, unlimited := layout.SplitCapDeclared(ctx, env)
	var anchor *layout.Anchor
	if declared {
		anchor = layout.SplitAnchorDeclared(raw, me, ids, cols, overflow, cap, unlimited, min)
	} else {
		anchor = layout.SplitAnchor(raw, me, ids, cap, min)
	}
	plan := layoutPlan{Cap: cap, MinPane: min, Candidates: []layoutCandidate{}}
	placement, direction, reason := "split", "right", "largest-area"
	anchorID := me
	if anchor != nil {
		if anchor.Overflow != "" {
			placement, reason = "herd", anchor.Overflow
			plan.Grid = nil
		} else {
			anchorID, direction = anchor.Anchor, anchor.Direction
		}
	}
	for _, p := range layout.LayoutFractions(raw) {
		ok := p.PaneID == me
		for _, id := range ids {
			if p.PaneID == id {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		w, h := p.Width, p.Height
		w, h = math.Round(w*1000)/1000, math.Round(h*1000)/1000
		if math.IsNaN(w) || math.IsInf(w, 0) || math.IsNaN(h) || math.IsInf(h, 0) {
			continue
		}
		plan.Candidates = append(plan.Candidates, layoutCandidate{p.PaneID, p.PaneID == me, w, h})
	}
	plan.Placement, plan.Reason = placement, reason
	if placement == "split" {
		plan.Anchor, plan.Direction = &anchorID, &direction
		n := len(plan.Candidates) + 1
		g := layout.GridShapeFor(ctx, env, n)
		rows := make([]any, len(g.Rows))
		for i, row := range g.Rows {
			rows[i] = row
		}
		plan.Grid = jsonjs.O("cells", n, "cols", g.Cols, "rows_per_col", rows)
	}
	candidates := make([]any, len(plan.Candidates))
	for i, candidate := range plan.Candidates {
		candidates[i] = jsonjs.O("pane_id", candidate.PaneID, "caller", candidate.Caller, "width", candidate.Width, "height", candidate.Height)
	}
	// An uncapped declared tab reports cap null: 0 is an explicit
	// split_max_panes=0, the opposite of no cap.
	var capValue any = plan.Cap
	if unlimited {
		capValue = nil
	}
	var anchorValue, directionValue any = nil, nil
	if plan.Anchor != nil {
		anchorValue = *plan.Anchor
	}
	if plan.Direction != nil {
		directionValue = *plan.Direction
	}
	result := jsonjs.O("placement", plan.Placement, "anchor", anchorValue, "direction", directionValue, "reason", plan.Reason,
		"cap", capValue, "min_pane", plan.MinPane, "candidates", candidates, "grid", plan.Grid)
	_, _ = fmt.Fprintf(platform.Stdout, "%s\n", jsonjs.Stringify(result))
	return 0
}
