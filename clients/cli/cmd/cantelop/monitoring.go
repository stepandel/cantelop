package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/render"
)

// Keep the response as JSON so new monitoring fields and numeric precision are
// preserved for scripts, even before the CLI learns how to summarize them.
func runMonitoring(ctx context.Context, kind string, args []string, output io.Writer, client *http.Client) error {
	usage := "usage: cantelop " + kind + " [ID] [--json] [-config cantelop.json]"
	options, err := parseOperationalOptions(args, false, -1, usage)
	if err != nil {
		return err
	}
	if len(options.positionals) > 1 {
		return errors.New(usage)
	}
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/"+kind, "", nil, &raw); err != nil {
		return fmt.Errorf("read %s: %w", kind, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("invalid monitoring response: %w", err)
	}
	if err := validateMonitoring(kind, document, appID); err != nil {
		return err
	}
	var value any = document
	if len(options.positionals) == 1 {
		id := options.positionals[0]
		if kind == "sandboxes" {
			value = nil
			for _, item := range document["sandboxes"].([]any) {
				if item.(map[string]any)["id"] == id {
					value = item
					break
				}
			}
			if value == nil {
				return fmt.Errorf("sandbox %q is not in the current snapshot", id)
			}
		} else {
			found := false
			for _, key := range []string{"requests", "unlinked_messages"} {
				selected := make([]any, 0)
				for _, item := range document[key].([]any) {
					if monitoringContainsID(item, id) {
						selected = append(selected, item)
						found = true
					}
				}
				document[key] = selected
			}
			if !found {
				return fmt.Errorf("ID %q is not in the recent trace window", id)
			}
		}
	}
	if options.jsonOutput {
		return render.WriteJSON(output, value)
	}
	if kind == "sandboxes" && len(options.positionals) == 0 {
		return writeSandboxSummary(output, document)
	}
	return writeMonitoringTree(output, value, 0)
}

func validateMonitoring(kind string, document map[string]any, appID string) error {
	invalid := errors.New("control plane returned invalid monitoring data")
	if document == nil {
		return invalid
	}
	keys := []string{"sandboxes", "sessions", "workspaces"}
	if kind == "traces" {
		if document["app_id"] != appID {
			return invalid
		}
		keys = []string{"requests", "unlinked_messages"}
	} else {
		if _, ok := document["ready"].(bool); !ok {
			return invalid
		}
		if observed, ok := document["observed_at"].(string); !ok || observed == "" {
			return invalid
		}
	}
	for _, key := range keys {
		items, ok := document[key].([]any)
		if !ok {
			return invalid
		}
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				return invalid
			}
			if id, ok := object["id"].(string); !ok || id == "" {
				return invalid
			}
			if kind == "sandboxes" && object["app_id"] != appID {
				return invalid
			}
		}
	}
	return nil
}

// Selecting a nested trace/span/message retains its request context.
func monitoringContainsID(value any, id string) bool {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"id", "trace_id", "span_id"} {
			if item[key] == id {
				return true
			}
		}
		for key, child := range item {
			if key != "attributes" && monitoringContainsID(child, id) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if monitoringContainsID(child, id) {
				return true
			}
		}
	}
	return false
}

func writeSandboxSummary(output io.Writer, document map[string]any) error {
	if _, err := fmt.Fprintf(output, "Observed: %s  Ready: %v\n", monitoringText(document["observed_at"]), document["ready"]); err != nil {
		return err
	}
	writer := render.NewTable(output)
	fmt.Fprintln(writer, "SANDBOX\tSTATE\tWORKSPACE\tBUILD\tPROFILE\tVCPUS\tMEMORY MiB\tUSED BYTES\tCPU us\tSTARTUP ms\tFAILURE")
	for _, value := range document["sandboxes"].([]any) {
		item := value.(map[string]any)
		usage, _ := item["usage"].(map[string]any)
		startup, _ := item["startup"].(map[string]any)
		failure, _ := item["failure"].(map[string]any)
		fields := []any{item["id"], item["state"], item["workspace_id"], item["build_id"], item["profile"], item["vcpus"], item["memory_mib"], usage["memory_current_bytes"], usage["cpu_usage_us"], startup["duration_ms"], failure["reason"]}
		text := make([]string, len(fields))
		for i, field := range fields {
			text[i] = monitoringText(field)
		}
		fmt.Fprintln(writer, strings.Join(text, "\t"))
	}
	return writer.Flush()
}

// Quote remote strings so logs and attributes cannot inject terminal controls.
func monitoringText(value any) string {
	if value == nil {
		return "-"
	}
	if text, ok := value.(string); ok {
		quoted := strconv.Quote(text)
		return quoted[1 : len(quoted)-1]
	}
	return fmt.Sprint(value)
}

func writeMonitoringTree(output io.Writer, value any, depth int) error {
	indent := strings.Repeat("  ", depth)
	switch item := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := item[key]
			switch child.(type) {
			case map[string]any, []any:
				if _, err := fmt.Fprintf(output, "%s%s:\n", indent, monitoringText(key)); err != nil {
					return err
				}
				if err := writeMonitoringTree(output, child, depth+1); err != nil {
					return err
				}
			default:
				if _, err := fmt.Fprintf(output, "%s%s: %s\n", indent, monitoringText(key), monitoringText(child)); err != nil {
					return err
				}
			}
		}
	case []any:
		if len(item) == 0 {
			_, err := fmt.Fprintln(output, indent+"(none)")
			return err
		}
		for i, child := range item {
			if _, err := fmt.Fprintf(output, "%s[%d]\n", indent, i+1); err != nil {
				return err
			}
			if err := writeMonitoringTree(output, child, depth+1); err != nil {
				return err
			}
		}
	default:
		_, err := fmt.Fprintln(output, indent+monitoringText(item))
		return err
	}
	return nil
}
