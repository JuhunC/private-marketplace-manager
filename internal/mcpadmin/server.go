// Package mcpadmin exposes Private Marketplace Manager diagnostics and repairs over MCP.
package mcpadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuhunC/private-marketplace-manager/internal/adminapi"
	"github.com/JuhunC/private-marketplace-manager/internal/vsix"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type EmptyInput struct{}

type InventoryInput struct {
	ID     string `json:"id,omitempty" jsonschema:"optional exact extension ID such as ms-vscode.hexeditor"`
	Limit  int    `json:"limit,omitempty" jsonschema:"number of packages to return, from 1 to 500; defaults to 100"`
	Offset int    `json:"offset,omitempty" jsonschema:"zero-based inventory offset"`
}

type CheckInput struct {
	Keys []string `json:"keys" jsonschema:"one to 1000 package keys in id@version@platform form"`
}

type UploadInput struct {
	Path string `json:"path" jsonschema:"absolute or working-directory-relative path to one local VSIX file"`
}

type Finding struct {
	Severity        string `json:"severity"`
	Code            string `json:"code"`
	Summary         string `json:"summary"`
	Evidence        string `json:"evidence,omitempty"`
	SuggestedAction string `json:"suggestedAction,omitempty"`
}

type Analysis struct {
	Healthy            bool           `json:"healthy"`
	Status             map[string]any `json:"status,omitempty"`
	StatusCounts       map[string]int `json:"statusCounts"`
	InventoryExamined  int            `json:"inventoryExamined"`
	InventoryTotal     int            `json:"inventoryTotal"`
	InventoryTruncated bool           `json:"inventoryTruncated"`
	RecentSyncRuns     int            `json:"recentSyncRuns"`
	FailedSyncRuns     int            `json:"failedSyncRuns"`
	Findings           []Finding      `json:"findings"`
}

func boolPointer(v bool) *bool { return &v }

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
}

func additive(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: false, IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
}

func New(api *adminapi.Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:       "private-marketplace-manager",
		Title:      "Private Marketplace Administrator",
		Version:    version,
		WebsiteURL: "https://github.com/JuhunC/private-marketplace-manager",
	}, &mcp.ServerOptions{
		Instructions: "Analyze with manager_analyze before repairs. Reconciliation never deletes VSIX files. Upload only a VSIX path the administrator has reviewed; the manager will validate it and never overwrite different bytes for an existing identity.",
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_health", Title: "Check manager health", Annotations: readOnly("Check manager health"),
		Description: "Check liveness, writable storage/database readiness, API compatibility, package count, bytes, and upload limit.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		out := map[string]any{}
		for name, path := range map[string]string{"liveness": "/health/live", "readiness": "/health/ready", "status": "/api/v1/status"} {
			var value map[string]any
			if e := api.JSON(ctx, http.MethodGet, path, nil, &value); e != nil {
				return nil, nil, fmt.Errorf("%s check failed: %w", name, e)
			}
			out[name] = value
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_inventory", Title: "List extension inventory", Annotations: readOnly("List extension inventory"),
		Description: "List stored, missing, pending, or conflicting package records with version, platform, filename, hash, and provenance metadata.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in InventoryInput) (*mcp.CallToolResult, any, error) {
		if in.ID != "" && !vsix.ValidID(in.ID) {
			return nil, nil, fmt.Errorf("id must be publisher.extension")
		}
		if in.Limit == 0 {
			in.Limit = 100
		}
		if in.Limit < 1 || in.Limit > 500 || in.Offset < 0 {
			return nil, nil, fmt.Errorf("limit must be 1..500 and offset must be non-negative")
		}
		q := url.Values{"limit": {fmt.Sprint(in.Limit)}, "offset": {fmt.Sprint(in.Offset)}}
		if in.ID != "" {
			q.Set("id", strings.ToLower(in.ID))
		}
		var out map[string]any
		if e := api.JSON(ctx, http.MethodGet, "/api/v1/extensions?"+q.Encode(), nil, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_check_packages", Title: "Check package files", Annotations: readOnly("Check package files"),
		Description: "Verify that selected package keys are recorded as stored and still exist as regular files with the expected size.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CheckInput) (*mcp.CallToolResult, any, error) {
		if len(in.Keys) < 1 || len(in.Keys) > 1000 {
			return nil, nil, fmt.Errorf("provide one to 1000 package keys")
		}
		var out map[string]any
		if e := api.JSON(ctx, http.MethodPost, "/api/v1/extensions/check", map[string]any{"keys": in.Keys}, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_audit_events", Title: "Read audit events", Annotations: readOnly("Read audit events"),
		Description: "Return the latest 100 manager audit events, including rejected uploads, conflicts, reconciliation, and stored packages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		var out []map[string]any
		if e := api.JSON(ctx, http.MethodGet, "/api/v1/audit-events", nil, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_sync_runs", Title: "Read synchronization runs", Annotations: readOnly("Read synchronization runs"),
		Description: "Return the latest 100 client-reported synchronization runs and their per-package failures.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		var out []map[string]any
		if e := api.JSON(ctx, http.MethodGet, "/api/v1/sync-runs", nil, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_analyze", Title: "Analyze marketplace issues", Annotations: readOnly("Analyze marketplace issues"),
		Description: "Run a bounded diagnostic review of readiness, API status, inventory state, and recent synchronization failures, returning findings and suggested repair actions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, Analysis, error) {
		return nil, analyze(ctx, api), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_reconcile_storage", Title: "Reconcile storage inventory", Annotations: additive("Reconcile storage inventory"),
		Description: "Rescan the designated VSIX directory, inventory valid external files, recover pending publications, and mark absent records missing. It never deletes VSIX files or resolves different-byte conflicts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		var out map[string]any
		if e := api.JSON(ctx, http.MethodPost, "/api/v1/admin/reconcile", map[string]any{}, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "manager_upload_vsix", Title: "Upload or restore a VSIX", Annotations: additive("Upload or restore a VSIX"),
		Description: "Validate one local VSIX and upload it idempotently. This can restore a missing package; it never replaces different bytes for the same extension, version, and platform.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UploadInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, nil, fmt.Errorf("path is required")
		}
		out, e := api.UploadVSIX(ctx, in.Path)
		if e != nil {
			return nil, nil, e
		}
		b, _ := json.Marshal(out)
		var result map[string]any
		_ = json.Unmarshal(b, &result)
		return nil, result, nil
	})

	return s
}

func analyze(ctx context.Context, api *adminapi.Client) Analysis {
	out := Analysis{Healthy: true, StatusCounts: map[string]int{}, Findings: []Finding{}}
	suppressedFindings := 0
	addFinding := func(f Finding) {
		if len(out.Findings) < 200 {
			out.Findings = append(out.Findings, f)
		} else {
			suppressedFindings++
		}
	}
	var ready map[string]any
	if e := api.JSON(ctx, http.MethodGet, "/health/ready", nil, &ready); e != nil {
		out.Healthy = false
		addFinding(Finding{Severity: "critical", Code: "manager_not_ready", Summary: "Manager readiness check failed", Evidence: e.Error(), SuggestedAction: "Check container logs, database access, extension-directory permissions, and free space."})
	}
	if e := api.JSON(ctx, http.MethodGet, "/api/v1/status", nil, &out.Status); e != nil {
		out.Healthy = false
		addFinding(Finding{Severity: "critical", Code: "status_unavailable", Summary: "Authenticated manager status is unavailable", Evidence: e.Error(), SuggestedAction: "Check the server URL, TLS trust, API token, and manager logs."})
		return out
	}
	const maximum = 5000
	for offset := 0; offset < maximum; offset += 500 {
		var page struct {
			Packages []vsix.Package `json:"packages"`
			Total    int            `json:"total"`
		}
		path := fmt.Sprintf("/api/v1/extensions?limit=500&offset=%d", offset)
		if e := api.JSON(ctx, http.MethodGet, path, nil, &page); e != nil {
			out.Healthy = false
			addFinding(Finding{Severity: "critical", Code: "inventory_unavailable", Summary: "Inventory could not be read", Evidence: e.Error()})
			break
		}
		out.InventoryTotal = page.Total
		out.InventoryExamined += len(page.Packages)
		for _, p := range page.Packages {
			out.StatusCounts[p.Status]++
			if p.Status != "stored" {
				severity := "warning"
				action := "Run manager_reconcile_storage, then re-upload the original VSIX if it remains missing."
				if p.Status == "conflict" {
					severity = "critical"
					action = "Inspect the conflicting files and hashes manually; the manager will not choose or overwrite either version."
				}
				addFinding(Finding{Severity: severity, Code: "package_" + p.Status, Summary: p.Key() + " is " + p.Status, Evidence: p.Filename, SuggestedAction: action})
			}
		}
		if offset+len(page.Packages) >= page.Total || len(page.Packages) == 0 {
			break
		}
	}
	out.InventoryTruncated = out.InventoryExamined < out.InventoryTotal
	if out.InventoryTruncated {
		addFinding(Finding{Severity: "info", Code: "inventory_truncated", Summary: "Automated analysis examined the first 5000 package records", Evidence: fmt.Sprintf("%d total", out.InventoryTotal), SuggestedAction: "Use manager_inventory with an extension ID or pagination to inspect the remainder."})
	}
	var runs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Failed int    `json:"failed"`
	}
	if e := api.JSON(ctx, http.MethodGet, "/api/v1/sync-runs", nil, &runs); e == nil {
		out.RecentSyncRuns = len(runs)
		for _, run := range runs {
			if run.Failed > 0 || run.Status == "partial" {
				out.FailedSyncRuns++
			}
		}
		if out.FailedSyncRuns > 0 {
			addFinding(Finding{Severity: "warning", Code: "sync_failures", Summary: fmt.Sprintf("%d recent synchronization runs contain failures", out.FailedSyncRuns), SuggestedAction: "Use manager_sync_runs to inspect package errors, correct connectivity or source issues, then rerun marketplace-sync."})
		}
	} else {
		addFinding(Finding{Severity: "warning", Code: "sync_history_unavailable", Summary: "Synchronization history could not be read", Evidence: e.Error()})
	}
	if suppressedFindings > 0 {
		out.Findings = append(out.Findings, Finding{Severity: "info", Code: "findings_truncated", Summary: fmt.Sprintf("%d additional findings were omitted", suppressedFindings), SuggestedAction: "Use manager_inventory filters and pagination to inspect specific records."})
	}
	if len(out.Findings) == 0 {
		out.Findings = append(out.Findings, Finding{Severity: "info", Code: "no_issues_found", Summary: "No manager readiness, inventory-state, or recent sync-run issues were found."})
	}
	return out
}
