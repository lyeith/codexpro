package activity

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Timeline struct {
	StartMS, EndMS, BinMS int64
	BinCount, ActionCount int
	Ticks                 []int64
	Lanes                 []TimelineLane
}
type TimelineLane struct {
	Key, ProjectID, Label string
	IDs                   []string
	LatestMS              int64
	Total                 int
	Bins                  []TimelineBin
}
type TimelineBin struct {
	Index                            int
	StartMS, EndMS                   int64
	Count, Mutating, Failed, Blocked int
	Tools                            []ToolCount
}
type ToolCount struct {
	Name  string
	Count int
}
type timedAction struct {
	view map[string]any
	at   int64
}

func laneFor(a map[string]any) (key, project, label string) {
	switch stringValue(a["attribution"]) {
	case "server":
		return "__server__", "", "Server"
	case "unattributed":
		return "__unattributed__", "", "Unattributed"
	case "unknown":
		return "__unknown__", "", "Unknown project id (not in catalog)"
	}
	project = stringValue(a["projectId"])
	key = project
	if key == "" {
		key = "__unattributed__"
	}
	return key, project, stringValue(a["projectLabel"])
}
func timelineBinLabel(ms int64) string {
	minutes := int64(math.Round(float64(ms) / 60000))
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	if minutes < 1440 {
		return fmt.Sprintf("%d h", int64(math.Round(float64(minutes)/60)))
	}
	return fmt.Sprintf("%d d", int64(math.Round(float64(minutes)/1440)))
}
func buildTimeline(actions []map[string]any, now int64, limit int) *Timeline {
	if limit < 1 {
		limit = 1
	}
	lanes := map[string][]timedAction{}
	order := []string{}
	for _, a := range actions {
		at, ok := parseTime(a["finishedAt"])
		if !ok || at.UnixMilli() > now {
			continue
		}
		key, _, _ := laneFor(a)
		list, exists := lanes[key]
		if !exists {
			order = append(order, key)
		}
		candidate := timedAction{a, at.UnixMilli()}
		index := sort.Search(len(list), func(i int) bool {
			return candidate.at > list[i].at || candidate.at == list[i].at && number(a["sequence"]) > number(list[i].view["sequence"])
		})
		if index >= limit {
			continue
		}
		list = append(list, timedAction{})
		copy(list[index+1:], list[index:])
		list[index] = candidate
		if len(list) > limit {
			list = list[:limit]
		}
		lanes[key] = list
	}
	timed := []timedAction{}
	for _, key := range order {
		timed = append(timed, lanes[key]...)
	}
	if len(timed) == 0 {
		return nil
	}
	earliest := now
	for _, item := range timed {
		if item.at < earliest {
			earliest = item.at
		}
	}
	duration := max(int64(3600000), now-earliest)
	binMS := int64(10080 * 60000)
	for _, minutes := range []int64{5, 10, 15, 30, 60, 120, 180, 360, 720, 1440, 2880, 10080} {
		if float64(minutes*60000) >= float64(duration)/140 {
			binMS = minutes * 60000
			break
		}
	}
	start := int64(math.Floor(float64(earliest)/float64(binMS))) * binMS
	end := int64(math.Ceil(float64(now+1)/float64(binMS))) * binMS
	model := &Timeline{StartMS: start, EndMS: end, BinMS: binMS, BinCount: max(1, int((end-start)/binMS)), ActionCount: len(timed), Ticks: []int64{}, Lanes: []TimelineLane{}}
	type buildingLane struct {
		lane TimelineLane
		ids  map[string]bool
		bins map[int]*TimelineBin
	}
	built := map[string]*buildingLane{}
	order = []string{}
	for _, item := range timed {
		a := item.view
		key, project, label := laneFor(a)
		lane := built[key]
		if lane == nil {
			lane = &buildingLane{lane: TimelineLane{Key: key, ProjectID: project, Label: label, IDs: []string{}, LatestMS: item.at, Bins: []TimelineBin{}}, ids: map[string]bool{}, bins: map[int]*TimelineBin{}}
			built[key] = lane
			order = append(order, key)
		}
		if a["attribution"] == "unknown" && stringValue(a["projectId"]) != "" {
			lane.ids[stringValue(a["projectId"])] = true
		}
		index := min(model.BinCount-1, max(0, int((item.at-start)/binMS)))
		bin := lane.bins[index]
		if bin == nil {
			bin = &TimelineBin{Index: index, StartMS: start + int64(index)*binMS, EndMS: start + int64(index+1)*binMS, Tools: []ToolCount{}}
			lane.bins[index] = bin
		}
		bin.Count++
		if boolean(a["mutating"]) {
			bin.Mutating++
		}
		if a["status"] == "failed" || a["status"] == "timed_out" {
			bin.Failed++
		}
		if a["status"] == "blocked" || a["status"] == "cancelled" {
			bin.Blocked++
		}
		tool := stringValue(a["toolName"])
		found := false
		for i := range bin.Tools {
			if bin.Tools[i].Name == tool {
				bin.Tools[i].Count++
				found = true
				break
			}
		}
		if !found {
			bin.Tools = append(bin.Tools, ToolCount{tool, 1})
		}
		lane.lane.Total++
		lane.lane.LatestMS = max(lane.lane.LatestMS, item.at)
	}
	tickMS := int64(10080 * 60000)
	for _, minutes := range []int64{30, 60, 180, 360, 720, 1440, 2880, 10080} {
		if float64(end-start)/float64(minutes*60000) <= 8 {
			tickMS = minutes * 60000
			break
		}
	}
	for tick := int64(math.Ceil(float64(start)/float64(tickMS))) * tickMS; tick < end; tick += tickMS {
		if tick > start {
			model.Ticks = append(model.Ticks, tick)
		}
	}
	for _, key := range order {
		building := built[key]
		lane := building.lane
		for id := range building.ids {
			lane.IDs = append(lane.IDs, id)
		}
		sort.Strings(lane.IDs)
		for _, bin := range building.bins {
			sort.SliceStable(bin.Tools, func(i, k int) bool { return bin.Tools[i].Count > bin.Tools[k].Count })
			lane.Bins = append(lane.Bins, *bin)
		}
		sort.Slice(lane.Bins, func(i, k int) bool { return lane.Bins[i].Index < lane.Bins[k].Index })
		model.Lanes = append(model.Lanes, lane)
	}
	sort.SliceStable(model.Lanes, func(i, k int) bool {
		if model.Lanes[i].LatestMS == model.Lanes[k].LatestMS {
			return model.Lanes[i].Label < model.Lanes[k].Label
		}
		return model.Lanes[i].LatestMS > model.Lanes[k].LatestMS
	})
	return model
}
func binSummary(bin TimelineBin) string {
	tools := []string{}
	for i, item := range bin.Tools {
		if i >= 6 {
			break
		}
		s := item.Name
		if item.Count > 1 {
			s += fmt.Sprintf("×%d", item.Count)
		}
		tools = append(tools, s)
	}
	parts := []string{plural(float64(bin.Count), "action"), strings.Join(tools, ", ")}
	if bin.Mutating > 0 {
		parts = append(parts, fmt.Sprintf("%d mutating", bin.Mutating))
	}
	if bin.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", bin.Failed))
	}
	if bin.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked/cancelled", bin.Blocked))
	}
	return join(parts...)
}
func renderTimeline(model *Timeline, note string) string {
	heading := `<div class="section-heading"><div><span class="eyebrow">Cross-project activity</span><h2>Activity timeline</h2></div><span>` + escape(note) + `</span></div>`
	if model == nil {
		return `<section class="dashboard-section timeline-panel">` + heading + `<p class="empty">Commands will appear here once debug activity is recorded.</p></section>`
	}
	percent := func(value int64) string {
		return fmt.Sprintf("%.3f", float64(value-model.StartMS)/float64(model.EndMS-model.StartMS)*100)
	}
	var ticks, grid, lanes strings.Builder
	for _, tick := range model.Ticks {
		value := timestamp(time.UnixMilli(tick))
		fmt.Fprintf(&ticks, `<span class="timeline-tick" data-refresh-key="tick:%s" style="left:%s%%"><time datetime="%s" data-local-axis>%s</time><i></i></span>`, escape(value), percent(tick), escape(value), escape(value))
		fmt.Fprintf(&grid, `<i class="timeline-grid" data-refresh-key="grid:%d" style="left:%s%%"></i>`, tick, percent(tick))
	}
	width := 100 / float64(model.BinCount)
	for _, lane := range model.Lanes {
		var cells strings.Builder
		for _, bin := range lane.Bins {
			summary := binSummary(bin)
			start, end := timestamp(time.UnixMilli(bin.StartMS)), timestamp(time.UnixMilli(bin.EndMS))
			tone := "good"
			if bin.Failed > 0 {
				tone = "bad"
			} else if bin.Blocked > 0 {
				tone = "warn"
			}
			if bin.Mutating > 0 {
				tone += " has-mutation"
			}
			fill := 0.4
			if bin.Count >= 9 {
				fill = 1
			} else if bin.Count >= 4 {
				fill = 0.78
			} else if bin.Count >= 2 {
				fill = 0.58
			}
			title := start + " – " + end + " · " + summary
			fmt.Fprintf(&cells, `<span class="timeline-cell %s" style="left:%.3f%%;width:%.3f%%;--fill:%v" data-refresh-key="bin:%s" data-bin-start="%s" data-bin-end="%s" data-bin-summary="%s" title="%s"><span class="visually-hidden">%s</span></span>`, tone, float64(bin.Index)*width, width, fill, escape(start), escape(start), escape(end), escape(summary), escape(title), escape(lane.Label+": "+title))
		}
		code := lane.ProjectID
		if code == "" {
			code = "global"
			if len(lane.IDs) > 0 {
				code = strings.Join(lane.IDs, ", ")
			}
		}
		fmt.Fprintf(&lanes, `<div class="timeline-lane" data-refresh-key="lane:%s"><div class="timeline-label"><strong title="%s">%s</strong><code>%s · %s</code></div><div class="timeline-track">%s%s</div></div>`, escape(lane.Key), escape(lane.Label), escape(lane.Label), escape(code), escape(plural(float64(lane.Total), "action")), grid.String(), cells.String())
	}
	start, end := timestamp(time.UnixMilli(model.StartMS)), timestamp(time.UnixMilli(model.EndMS))
	return `<section class="dashboard-section timeline-panel">` + heading + `<div class="timeline-scroll"><div class="timeline-chart"><div class="timeline-axis"><div></div><div class="timeline-axis-track">` + ticks.String() + `</div></div>` + lanes.String() + `</div></div><p class="timeline-range"><span class="timeline-legend"><i class="good"></i>ok <i class="warn"></i>blocked <i class="bad"></i>failed · darker = more actions · underline = wrote files</span><time datetime="` + escape(start) + `" data-local-time>` + escape(start) + `</time><span>to</span><time datetime="` + escape(end) + `" data-local-time>` + escape(end) + `</time></p></section>`
}
