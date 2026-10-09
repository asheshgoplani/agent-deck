package telemetry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// propType is the published one-line type of a property.
func (p Prop) propType() string {
	switch p.Kind {
	case KindEnum:
		return "enum: " + codeList(p.Values)
	case KindBucket:
		return "bucket `" + string(p.Bucket) + "`"
	case KindBitmask:
		return fmt.Sprintf("bitmask (%d bits)", p.Bits)
	case KindHash:
		return fmt.Sprintf("%d hex", p.HexLen)
	case KindPattern:
		return "pattern `" + p.Pattern.String() + "`"
	case KindInt:
		return fmt.Sprintf("int %d-%d", p.Min, p.Max)
	}
	return string(p.Kind)
}

// SchemaMarkdown renders the published field list. TELEMETRY.md embeds this
// output verbatim between the schema markers; a test keeps them equal.
func SchemaMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schema version: %d. Detailed events carry the envelope; install.tick uses only its separate allow-list below.\n\n", SchemaVersion)
	b.WriteString("#### Envelope (detailed events)\n\n| Property | Type | Notes |\n|---|---|---|\n")
	for _, p := range Envelope {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.Key, cell(p.propType()), cell(p.Doc))
	}
	b.WriteString("\nPostHog additionally receives `$process_person_profile: false`, `$geoip_disable: true` and `$lib: agent-deck` on every detailed event.\n")
	b.WriteString("\n#### Bucket edges\n\nLower edge inclusive, upper edge exclusive.\n\n| Bucket | Values |\n|---|---|\n")
	for _, bk := range bucketOrder {
		fmt.Fprintf(&b, "| `%s` | %s |\n", bk, cell(codeList(bucketLabels[bk])))
	}
	fmt.Fprintf(&b, "\nTool bitmask order: %s; `other` = 31.\n", bitDoc(toolBits))
	fmt.Fprintf(&b, "\nConfig section bitmask order (`config_sections`): %s; any other section = 31.\n", bitDoc(ConfigSections))
	fmt.Fprintf(&b, "\nFunnel step bits (`milestones_before`): %s.\n", bitDoc(milestoneNames))
	b.WriteString("\n#### Events\n\n")
	for _, e := range Events {
		ships := "call sites " + e.Ships
		if e.Ships == shipsLater {
			ships = "planned, not emitted yet"
		}
		flags := []string{fmt.Sprintf("tier %d", e.Tier), ships}
		if e.Basic {
			flags = append(flags, "also at level basic")
		}
		if e.Rollup {
			flags = append(flags, "daily rollup")
		}
		fmt.Fprintf(&b, "**`%s`** (%s)", e.Name, strings.Join(flags, ", "))
		if e.Emitted != "" {
			fmt.Fprintf(&b, ": %s", e.Emitted)
		}
		b.WriteString("\n\n| Property | Type |\n|---|---|\n")
		for _, p := range e.Props {
			t := p.propType()
			if p.Doc != "" {
				t += " (" + p.Doc + ")"
			}
			fmt.Fprintf(&b, "| `%s` | %s |\n", p.Key, cell(t))
		}
		b.WriteString("\n")
	}
	b.WriteString(installTickSchemaMarkdown)
	return b.String()
}

func bitDoc(names []string) string {
	parts := make([]string, len(names))
	for i, t := range names {
		parts[i] = fmt.Sprintf("`%s` = %d", t, i)
	}
	return strings.Join(parts, ", ")
}

func codeList(values []string) string {
	return "`" + strings.Join(values, "`, `") + "`"
}

func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

type schemaPropJSON struct {
	Key     string   `json:"key"`
	Kind    Kind     `json:"kind"`
	Values  []string `json:"values,omitempty"`
	Bucket  string   `json:"bucket,omitempty"`
	Bits    int      `json:"bits,omitempty"`
	HexLen  int      `json:"hex_len,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Min     *int     `json:"min,omitempty"`
	Max     *int     `json:"max,omitempty"`
	Doc     string   `json:"doc,omitempty"`
}

type schemaEventJSON struct {
	Name     string           `json:"name"`
	Tier     int              `json:"tier"`
	Ships    string           `json:"ships"`
	Basic    bool             `json:"basic"`
	Rollup   bool             `json:"rollup"`
	Emitted  string           `json:"emitted,omitempty"`
	Question string           `json:"question,omitempty"`
	Props    []schemaPropJSON `json:"properties"`
}

func propJSON(p Prop) schemaPropJSON {
	out := schemaPropJSON{Key: p.Key, Kind: p.Kind, Values: p.Values, Bucket: string(p.Bucket), Bits: p.Bits, HexLen: p.HexLen, Doc: p.Doc}
	if p.Pattern != nil {
		out.Pattern = p.Pattern.String()
	}
	if p.Kind == KindInt {
		mn, mx := p.Min, p.Max
		out.Min, out.Max = &mn, &mx
	}
	return out
}

// SchemaJSON returns the full allow-list as indented JSON.
func SchemaJSON() ([]byte, error) {
	doc := struct {
		Schema      int                 `json:"schema"`
		InstallTick map[string]any      `json:"install_tick"`
		Envelope    []schemaPropJSON    `json:"envelope"`
		Buckets     map[string][]string `json:"buckets"`
		ToolBits    []string            `json:"tool_bits"`
		Events      []schemaEventJSON   `json:"events"`
	}{Schema: SchemaVersion, Buckets: map[string][]string{}, ToolBits: toolBits,
		InstallTick: map[string]any{
			"event": "install.tick", "detailed_envelope": false,
			"properties": map[string]any{"day": "YYYY-MM-DD", "v": "release version at reservation", "os": osValues, "arch": archValues, "consent_state": []string{"granted"}, "tick_id": "random 128-bit UUID-formatted nonce per local day", "$process_person_profile": false, "$geoip_disable": true},
			"uuid":       "tick_id", "distinct_id": "tick_id", "timestamp": "day at 12:00 labelled UTC", "counting": "DISTINCT tick_id per day",
		}}
	for _, p := range Envelope {
		doc.Envelope = append(doc.Envelope, propJSON(p))
	}
	for _, bk := range bucketOrder {
		doc.Buckets[string(bk)] = bucketLabels[bk]
	}
	for _, e := range Events {
		ej := schemaEventJSON{Name: e.Name, Tier: e.Tier, Ships: e.Ships, Basic: e.Basic, Rollup: e.Rollup, Emitted: e.Emitted, Question: e.Question}
		for _, p := range e.Props {
			ej.Props = append(ej.Props, propJSON(p))
		}
		doc.Events = append(doc.Events, ej)
	}
	return json.MarshalIndent(doc, "", "  ")
}

const installTickSchemaMarkdown = "#### Anonymous daily install tick\n" +
	"\n" +
	"`install.tick` is separate from the detailed envelope, at full and basic levels. Its complete properties are:\n" +
	"\n" +
	"| Property | Value |\n" +
	"|---|---|\n" +
	"| `day` | Local calendar day, YYYY-MM-DD |\n" +
	"| `v` | Release version when the daily nonce was reserved |\n" +
	"| `os` | Go GOOS at reservation, same allow-list as the detailed envelope; no OS version; absent on ticks reserved by older releases |\n" +
	"| `arch` | Go GOARCH at reservation, same allow-list as the detailed envelope; absent on ticks reserved by older releases |\n" +
	"| `consent_state` | `granted`; undecided and declined never send |\n" +
	"| `tick_id` | Random 128-bit daily nonce formatted as a UUID; reused on retries |\n" +
	"| `$process_person_profile` | `false` |\n" +
	"| `$geoip_disable` | `true` |\n" +
	"\n" +
	"The PostHog event `uuid` and required `distinct_id` both equal `tick_id`. No persistent install ID or detailed envelope is attached. Timestamp is the local day at 12:00 labelled UTC. The dashboard must count DISTINCT `tick_id` per `day`; retries may produce multiple rows. The nonce links only retries of one daily event.\n" +
	"\n" +
	"`os` and `arch` were added to the tick in schema 3 without a schema version change. This is a recorded decision: every detailed schema 3 event already sends both values under the same grant, and a bump would ask every install for consent again.\n" +
	"\n"
