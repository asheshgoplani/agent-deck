package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func creationFieldsFromFlags(fs *flag.FlagSet) []session.RemoteCreationField {
	fields := []session.RemoteCreationField{}
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "capabilities" {
			return
		}
		takesValue := true
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			takesValue = false
		}
		fields = append(fields, session.RemoteCreationField{Name: f.Name, TakesValue: takesValue})
	})
	return fields
}

func creationCommandFields(command string) []session.RemoteCreationField {
	var fields []session.RemoteCreationField
	inspect := func(fs *flag.FlagSet) { fields = creationFieldsFromFlags(fs) }
	switch command {
	case "add":
		handleAddCommand("", nil, inspect)
	case "launch":
		handleLaunchCommand("", nil, inspect)
	}
	return fields
}

func writeCreationCatalog(profile string, fs *flag.FlagSet, jsonOutput bool) {
	valid := jsonOutput && fs.NArg() == 0
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "capabilities" && f.Name != "json" {
			valid = false
		}
	})
	if !valid {
		fmt.Fprintln(os.Stderr, "Error: --capabilities requires --json and cannot be combined with creation arguments")
		os.Exit(2)
	}
	catalog, err := buildCreationCatalog(profile)
	if err != nil {
		NewCLIOutput(true, false).Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(catalog); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildCreationCatalog(profile string) (*session.RemoteCreationCatalog, error) {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil, err
	}
	catalog := &session.RemoteCreationCatalog{Version: 1, Commands: map[string][]session.RemoteCreationField{"add": creationCommandFields("add"), "launch": creationCommandFields("launch")}, DefaultTool: session.GetDefaultTool(), Tools: []session.RemoteCreationTool{}, Accounts: []string{}, MCPs: []string{}, Conductors: []session.RemoteCreationConductor{}}
	claudeDefaults := session.NewClaudeOptions(cfg)
	catalog.Defaults = map[string]bool{"skip_permissions": claudeDefaults.SkipPermissions, "auto_mode": claudeDefaults.AutoMode, "chrome": claudeDefaults.UseChrome, "teammate_mode": claudeDefaults.UseTeammateMode, "codex_yolo": cfg.Codex.YoloMode, "gemini_yolo": cfg.Gemini.YoloMode, "hermes_yolo": cfg.Hermes.YoloMode}
	names := append([]string{"", "claude", "gemini", "opencode", "codex", "pi", "copilot", "crush", "cursor", "hermes", "deepseek"}, session.GetCustomToolNames()...)
	for _, name := range session.FilterVisibleToolNames(names) {
		kind := name
		if session.IsClaudeCompatible(name) {
			kind = "claude"
		} else if session.IsCodexCompatible(name) {
			kind = "codex"
		}
		tool := session.RemoteCreationTool{Name: name, Kind: kind, Models: session.KnownModelIDsForTool(kind)}
		switch kind {
		case "claude":
			tool.DefaultModel = cfg.Claude.DefaultModel
		case "gemini":
			tool.DefaultModel = cfg.Gemini.DefaultModel
		case "opencode":
			tool.DefaultModel = cfg.OpenCode.DefaultModel
		}
		catalog.Tools = append(catalog.Tools, tool)
	}
	for name := range cfg.Profiles {
		if cfg.GetProfileClaudeConfigDir(name) != "" {
			catalog.Accounts = append(catalog.Accounts, name)
		}
	}
	sort.Strings(catalog.Accounts)
	for name := range session.GetAvailableMCPs() {
		catalog.MCPs = append(catalog.MCPs, name)
	}
	sort.Strings(catalog.MCPs)
	instances, _, err := readCreationRegistry(profile)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.IsConductor {
			catalog.Conductors = append(catalog.Conductors, session.RemoteCreationConductor{ID: inst.ID, Title: inst.Title})
		}
	}
	return catalog, nil
}

func validateCreationPaths(paths []string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("--additional-path cannot be empty")
		}
		if path == "~" || strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("additional project path %q: %w", path, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("additional project path %q is not a directory", path)
		}
		if seen[resolved] {
			return nil, fmt.Errorf("duplicate additional project path %q", path)
		}
		result = append(result, resolved)
		seen[resolved] = true
	}
	return result, nil
}

func validateCreationStartupQuery(query, tool, mode string, starts bool, extraArgs ...string) error {
	if query == "" {
		return nil
	}
	for _, arg := range extraArgs {
		name, _, _ := strings.Cut(arg, "=")
		if name == "--resume" || name == "-r" || name == "--continue" || name == "-c" {
			return fmt.Errorf("--startup-query cannot be combined with resume or continue arguments")
		}
	}
	if !session.IsClaudeCompatible(tool) {
		return fmt.Errorf("--startup-query requires a Claude-compatible tool")
	}
	if !starts {
		return fmt.Errorf("--startup-query requires immediate start; use launch or add --attach")
	}
	if mode != "" && mode != "new" {
		return fmt.Errorf("--startup-query cannot be combined with resume or continue")
	}
	return nil
}

func applyCreationExtras(inst *session.Instance, query string, additional []string, branch string, newBranch bool, location string) (err error) {
	inst.StartupQuery = query
	if len(additional) == 0 {
		return nil
	}
	allPaths := append([]string{inst.ProjectPath}, additional...)
	primary, err := filepath.EvalSymlinks(inst.ProjectPath)
	if err != nil {
		return err
	}
	for _, path := range additional {
		if path == primary {
			return fmt.Errorf("additional project path duplicates primary project %q", path)
		}
	}
	if err := validateMultiRepoCreation(inst.ProjectPath, additional, branch, newBranch, location); err != nil {
		return err
	}
	// Resolved for inst.ProjectPath so directory-local .agent-deck/config.toml
	// overrides (#2093) apply (sparse_checkout in particular; default_location
	// and path_template do not apply to multi-repo worktrees, whose layout is
	// owned by the host data directory). Only branch mode consults them.
	var wtSettings session.WorktreeSettings
	if branch != "" {
		wtSettings, err = session.GetWorktreeSettingsForDir(inst.ProjectPath)
		if err != nil {
			return fmt.Errorf("invalid directory-local config: %w", err)
		}
		branch = wtSettings.ApplyBranchPrefix(branch)
	}
	root, err := agentpaths.EffectiveDataPath("multi-repo-worktrees", "multi-repo-worktrees")
	if err != nil {
		return err
	}
	parent := filepath.Join(root, inst.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(parent, 0o755); err != nil {
		return err
	}
	inst.MultiRepoEnabled = true
	inst.MultiRepoTempDir = parent
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanupOwnedCreationArtifacts(inst))
		}
	}()
	if branch != "" {
		result, createErr := session.CreateMultiRepoWorktreesStrictWithOptions(allPaths, parent, branch, wtSettings.SetupTimeout(), wtSettings.InheritSparseCheckout())
		inst.MultiRepoWorktrees = result.Worktrees
		if createErr != nil {
			return createErr
		}
		inst.ProjectPath = result.MappedPaths[0]
		inst.AdditionalPaths = result.MappedPaths[1:]
	} else {
		names := session.DeduplicateDirnames(allPaths)
		mapped := make([]string, len(allPaths))
		for i, path := range allPaths {
			mapped[i] = filepath.Join(parent, names[i])
			if err := os.Symlink(path, mapped[i]); err != nil {
				return err
			}
		}
		inst.ProjectPath = mapped[0]
		inst.AdditionalPaths = mapped[1:]
	}
	repoNames := make([]string, 0, len(inst.AllProjectPaths()))
	for _, path := range inst.AllProjectPaths() {
		repoNames = append(repoNames, filepath.Base(path))
	}
	if err := session.ApplyMultiRepoClaudeContext(inst.Tool, true, session.GetUserMCPRootPath(), parent, repoNames); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: multi-repo Claude context:", err)
	}
	if err := session.ApplyMultiRepoCodexContext(inst.Tool, true, parent); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: multi-repo Codex context:", err)
	}
	if tm := inst.GetTmuxSession(); tm != nil {
		tm.WorkDir = parent
	}
	return nil
}

// validateCreationOptions performs only reads and in-memory option application.
// Run it before mkdir, group reconciliation, worktree setup or registry writes.
func validateCreationOptions(tool, account, model, effort string, yolo bool, flags *claudeOptionFlags, mcps, plugins, channels, extraArgs []string) error {
	if tool == "" {
		tool = session.GetDefaultTool()
	}
	inst := &session.Instance{Tool: tool, Account: account}
	if err := inst.ValidateAccount(); err != nil {
		return err
	}
	if err := applyCLIModelOverride(inst, strings.TrimSpace(model)); err != nil {
		return err
	}
	if err := applyCLIEffortOverride(inst, strings.TrimSpace(effort)); err != nil {
		return err
	}
	if err := applyCLIYoloOverride(inst, yolo, cliFlagWasSet(flags.fs, "yolo", "gemini-yolo")); err != nil {
		return err
	}
	if err := applyCLIClaudeOptionFlags(inst, flags); err != nil {
		return err
	}
	if (len(plugins) > 0 || len(channels) > 0 || len(extraArgs) > 0) && !session.IsClaudeCompatible(tool) {
		return fmt.Errorf("--plugin, --channel and --extra-arg require a Claude-compatible tool")
	}
	if err := validatePluginFlags(plugins); err != nil {
		return err
	}
	for _, token := range extraArgs {
		if err := session.ValidateClaudeExtraArgToken(token); err != nil {
			return err
		}
	}
	if len(mcps) > 0 && !session.ToolSupportsMCPManager(tool) {
		return fmt.Errorf("tool %q does not support MCP attachment", tool)
	}
	available := session.GetAvailableMCPs()
	for _, name := range mcps {
		if _, ok := available[name]; !ok {
			return fmt.Errorf("MCP %q not found in host catalog", name)
		}
	}
	return nil
}

func validateStartupQueryCapacity(profile, group, parent, path string, noParent, inheritGroup, checkCapacity bool, resolvedGroup *string) error {
	if !checkCapacity && parent == "" && noParent {
		return nil
	}
	instances, groups, err := readCreationRegistry(profile)
	if err != nil {
		return err
	}
	selectedGroup := group
	if selectedGroup == "" {
		selectedGroup = session.GroupPathForProject(path)
	}
	var parentInst *session.Instance
	if parent != "" {
		var message string
		parentInst, message, _ = ResolveSession(parent, instances)
		if parentInst == nil {
			return fmt.Errorf("%s", message)
		}
	} else if !noParent {
		var unresolved string
		parentInst, unresolved = resolveAutoParentInstanceChecked(instances)
		if unresolved != "" {
			return fmt.Errorf("automatic parent %q could not be resolved; use --no-parent", unresolved)
		}
	}
	if parentInst != nil && parentInst.IsSubSession() {
		return fmt.Errorf("cannot create sub-session of a sub-session (single level only)")
	}
	if parentInst != nil && group == "" && (inheritGroup || selectedGroup == "") {
		selectedGroup = parentInst.GroupPath
	}
	if resolvedGroup != nil {
		*resolvedGroup = selectedGroup
	}
	tree := session.NewGroupTreeWithGroups(instances, groups)
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return err
	}
	tree.DefaultMaxConcurrent = cfg.GroupDefaults.MaxConcurrent
	session.ReconcileDeclarativeGroups(tree, cfg)
	if checkCapacity && session.ShouldQueue(instances, selectedGroup, session.GroupMaxConcurrent(tree, selectedGroup)) {
		return fmt.Errorf("startup query cannot be queued; retry when group capacity is available")
	}
	return nil
}

// normalizeCreationArgs uses the registered schema and preserves the positional
// terminator, including paths that begin with a dash.
func normalizeCreationArgs(fs *flag.FlagSet, args []string) []string {
	var flags, paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			paths = append(paths, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			paths = append(paths, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if inline {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if len(paths) > 0 {
		flags = append(flags, "--")
		flags = append(flags, paths...)
	}
	return flags
}

func validatePrimaryCreationPath(primary string, additional []string) error {
	if len(additional) == 0 {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(primary)
	if err != nil {
		resolved, err = filepath.Abs(primary)
		if err != nil {
			return err
		}
	}
	for _, path := range additional {
		if path == resolved {
			return fmt.Errorf("additional project path duplicates primary project %q", path)
		}
	}
	return nil
}

func validateMultiRepoCreation(primary string, additional []string, branch string, newBranch bool, location string) error {
	if len(additional) == 0 {
		return nil
	}
	allPaths := append([]string{primary}, additional...)
	if branch != "" {
		if location != "" {
			return fmt.Errorf("--location cannot be combined with multi-repo worktrees; layout is owned by the host data directory")
		}
		settings, err := session.GetWorktreeSettingsForDir(primary)
		if err != nil {
			return fmt.Errorf("invalid directory-local config: %w", err)
		}
		branch = settings.ApplyBranchPrefix(branch)
		if err := git.ValidateBranchName(branch); err != nil {
			return err
		}
		if newBranch {
			for _, path := range allPaths {
				backend, err := detectAndCreateBackend(path)
				if err == nil && backend.BranchExists(branch) {
					return fmt.Errorf("branch %q already exists in %s (remove --new-branch to reuse it)", branch, path)
				}
			}
		}
	}
	return nil
}

// readCreationRegistry never initializes or migrates the owner database. A
// genuinely blank SQLite file is a fresh registry, whereas partial schemas
// and unreadable databases remain visible failures.
func readCreationRegistry(profile string) ([]*session.Instance, []*session.GroupData, error) {
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return nil, nil, err
	}
	dbPath, err := session.GetDBPathForProfile(resolved)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	storage, err := session.NewLiveReadOnlyStorageWithProfile(resolved)
	if err != nil {
		return nil, nil, err
	}
	defer storage.Close()
	var tables int
	if err := storage.GetDB().DB().QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT GLOB 'sqlite_*'").Scan(&tables); err != nil {
		return nil, nil, err
	}
	if tables == 0 {
		return nil, nil, nil
	}
	return storage.LoadWithGroups()
}

// cleanupOwnedCreationArtifacts compensates only the workspace recorded on a
// newly created instance. It never removes original repository paths.
func cleanupOwnedCreationArtifacts(inst *session.Instance) error {
	var cleanupErrors []error
	for _, wt := range inst.MultiRepoWorktrees {
		registered, err := git.ListWorktrees(wt.RepoRoot)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect worktree %s: %w", wt.WorktreePath, err))
			continue
		}
		ownedPath := filepath.Clean(wt.WorktreePath)
		if resolved, err := filepath.EvalSymlinks(ownedPath); err == nil {
			ownedPath = resolved
		}
		exists := false
		for _, checkout := range registered {
			if filepath.Clean(checkout.Path) == ownedPath {
				exists = true
				break
			}
		}
		if !exists {
			continue
		}
		if err := git.RemoveWorktree(wt.RepoRoot, wt.WorktreePath, true); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove worktree %s: %w", wt.WorktreePath, err))
		}
	}
	// Keep recovery evidence if a worktree could not be unregistered.
	if len(cleanupErrors) == 0 {
		if err := inst.CleanupMultiRepoTempDir(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}
