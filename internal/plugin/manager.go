package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// hookBreakerThreshold trips a plugin's hook delivery after this many
// consecutive failures (e.g. a broken interpreter exiting 9009 on every
// beacon). Tripped plugins are skipped silently-ish until the cooldown
// passes, then one probe is allowed through.
const hookBreakerThreshold = 5
const hookBreakerCooldown = time.Minute

// pendingPlugin is one manifest discovered on disk, waiting for its turn in
// the dependency-ordered registration pass.
type pendingPlugin struct {
	root     string
	dir      string
	manifest *Manifest
}

// Manager loads, stores, and executes ForgeC2 plugins.
type Manager struct {
	db          *gorm.DB
	marketplace *Marketplace
	plugins     map[string]Plugin
	mu          sync.RWMutex
	pluginDir   string
	exec        *executor
	// hookFails counts consecutive hook failures per plugin name;
	// hookTrippedAt marks when the breaker tripped (cooldown probes).
	hookFails     map[string]int
	hookTrippedAt map[string]time.Time
	// trust holds the package-verification policy (trusted signing keys and
	// whether unsigned packages are refused).
	trust trustPolicy
	// loadFailures records the latest reason a discovered package could not be
	// registered. It is intentionally diagnostic only: failed packages remain
	// unavailable and are never treated as trusted.
	loadFailures map[string]string
}

// NewManager creates a new plugin manager backed by the given database.
func NewManager(database *gorm.DB) *Manager {
	return &Manager{
		db:            database,
		plugins:       make(map[string]Plugin),
		pluginDir:     "plugins",
		exec:          newExecutor(defaultMaxConcurrentPlugins),
		hookFails:     make(map[string]int),
		hookTrippedAt: make(map[string]time.Time),
		loadFailures:  make(map[string]string),
	}
}

// SetMaxConcurrent caps how many plugin processes may run simultaneously.
// Effective quota accessors. They exist so the status surface and the reload tests can
// read what the manager will actually enforce, instead of assuming a Set* call took.
func (m *Manager) MaxConcurrent() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.exec == nil {
		return 0
	}
	return cap(m.exec.slots)
}

func (m *Manager) MaxCPUSeconds() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.exec == nil {
		return 0
	}
	return int(m.exec.cpuQuota / time.Second)
}

func (m *Manager) MaxWriteMB() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.exec == nil {
		return 0
	}
	return int(m.exec.writeQuota >> 20)
}

// RequireSigned reports whether the trust policy refuses unsigned packages.
func (m *Manager) RequireSigned() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trust.RequireSigned
}

func (m *Manager) SetMaxConcurrent(n int) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exec = m.exec.withMaxConcurrent(n)
}

// SetMaxCPUSeconds caps how much CPU time one plugin run may spend across its
// whole process tree before the tree is killed (0 keeps the built-in default).
func (m *Manager) SetMaxCPUSeconds(secs int) {
	if secs <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exec = m.exec.withCPUQuota(time.Duration(secs) * time.Second)
}

// SetMaxWriteMB caps how many MiB one plugin run may write across its whole
// process tree before the tree is killed (0 keeps the built-in default).
func (m *Manager) SetMaxWriteMB(mb int) {
	if mb <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exec = m.exec.withWriteQuota(int64(mb) << 20)
}

// hookAllowed reports whether a plugin's hook may run. Tripped plugins are
// skipped until the cooldown passes, when a single probe goes through.
func (m *Manager) hookAllowed(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.hookFails[name] < hookBreakerThreshold {
		return true
	}
	return time.Since(m.hookTrippedAt[name]) > hookBreakerCooldown
}

// hookRecord accounts one hook delivery outcome for the breaker.
func (m *Manager) hookRecord(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Lazily initialized: a zero-value Manager (not via NewManager) must
	// not panic on its first hook delivery.
	if m.hookFails == nil {
		m.hookFails = make(map[string]int)
	}
	if m.hookTrippedAt == nil {
		m.hookTrippedAt = make(map[string]time.Time)
	}
	if err == nil {
		delete(m.hookFails, name)
		delete(m.hookTrippedAt, name)
		return
	}
	m.hookFails[name]++
	if m.hookFails[name] >= hookBreakerThreshold {
		m.hookTrippedAt[name] = time.Now()
	}
}

// SetMarketplace links the manager to an existing marketplace instance.
func (m *Manager) SetMarketplace(mp *Marketplace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketplace = mp
}

// LoadFromDisk walks a directory of plugin packages and registers each one.
func (m *Manager) LoadFromDisk(dir string) error {
	removeStrayPackageDirs(dir)
	m.mu.Lock()
	m.pluginDir = dir
	// Failures describe the current scan, not an append-only history. Clear
	// the snapshot before walking so a fixed or removed package stops being
	// reported as broken after the next reload.
	m.loadFailures = make(map[string]string)
	m.mu.Unlock()

	return m.loadFromDir(dir, dir)
}

// removeStrayPackageDirs deletes volatile trees that operator-side
// actions left inside plugin packages — currently the literal
// "%SystemDrive%" directory created when a copy runs through an
// unexpanded environment variable while a plugin's CWD is its own
// package dir (executor.go pins cmd.Dir to the package). Such trees
// are git-ignored, are never package payload, and would otherwise
// accumulate on disk. Best-effort: failures are logged, not fatal.
func removeStrayPackageDirs(root string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() || d.Name() != "%SystemDrive%" {
			return nil
		}
		if rerr := os.RemoveAll(path); rerr != nil {
			slog.Warn("Failed to remove stray plugin artifact", "dir", path, "err", rerr)
		} else {
			slog.Info("Removed stray artifact from plugin package", "dir", path)
		}
		return filepath.SkipDir
	})
}

// loadFromDir scans dir (and nested directories) for plugin packages. root is
// the directory LoadFromDisk was called with: it anchors manifest.Includes for
// every nested package, so a recursive descent must not change the trust root.
func (m *Manager) loadFromDir(dir, root string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var batch []pendingPlugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginDir := filepath.Join(dir, entry.Name())
		manifestPath := filepath.Join(pluginDir, "manifest.yaml")
		if _, err := os.Stat(manifestPath); err == nil {
			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				slog.Warn("Failed to load plugin manifest", "path", manifestPath, "err", err)
				m.recordLoadFailure(entry.Name(), err)
				continue
			}
			batch = append(batch, pendingPlugin{root: root, dir: pluginDir, manifest: manifest})
			continue
		}
		// Allow nested directories (e.g. plugins/example/portscan).
		if err := m.loadFromDir(pluginDir, root); err != nil {
			slog.Warn("Failed to load plugins from subdirectory", "dir", pluginDir, "err", err)
		}
	}
	if len(batch) == 0 {
		return nil
	}

	// Phase 2: dependency-aware ordering. Cycles, self-dependencies and
	// missing requirements are reported per plugin; everything that resolves
	// still loads (warn-and-continue semantics preserved from the original
	// per-entry loop).
	ordered, problems, rejected := topoSortPlugins(batch, m.loadedNames())
	for _, pErr := range problems {
		slog.Warn("Plugin dependency problem", "dir", dir, "err", pErr)
	}
	for name, reason := range rejected {
		m.recordLoadFailure(name, reason)
	}
	for _, p := range ordered {
		if err := m.registerAtDir(p.manifest, p.dir, p.root); err != nil {
			slog.Warn("Failed to register plugin", "name", p.manifest.Name, "err", err)
			m.recordLoadFailure(p.manifest.Name, err)
		}
	}
	return nil
}

func (m *Manager) recordLoadFailure(name string, err error) {
	if m == nil || name == "" || err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadFailures == nil {
		m.loadFailures = make(map[string]string)
	}
	m.loadFailures[name] = err.Error()
}

func (m *Manager) recordLoadSuccess(name string) {
	if m == nil || name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.loadFailures, name)
}

// LoadFailures returns a snapshot of packages discovered but rejected during
// the last disk load. The returned map is safe for callers to retain.
func (m *Manager) LoadFailures() map[string]string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.loadFailures))
	for name, reason := range m.loadFailures {
		out[name] = reason
	}
	return out
}

// loadedNames snapshots the names of currently registered plugins so a batch
// can depend on anything registered by an earlier LoadFromDisk/Register call.
func (m *Manager) loadedNames() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]bool, len(m.plugins))
	for name := range m.plugins {
		out[name] = true
	}
	return out
}

// topoSortPlugins orders a directory batch so dependencies register before
// their dependents. Problems (duplicate names, unknown/self dependencies,
// cycles) are returned individually and exclude only the implicated plugins.
// `loaded` satisfies requirements outside the batch.
func topoSortPlugins(batch []pendingPlugin, loaded map[string]bool) ([]pendingPlugin, []error, map[string]error) {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	byName := make(map[string]pendingPlugin, len(batch))
	var problems []error
	rejected := make(map[string]error)
	for _, p := range batch {
		if _, dup := byName[p.manifest.Name]; dup {
			err := fmt.Errorf("duplicate plugin name %q in the same directory", p.manifest.Name)
			problems = append(problems, err)
			rejected[p.manifest.Name] = err
			continue
		}
		byName[p.manifest.Name] = p
	}

	state := make(map[string]int, len(byName))
	failed := make(map[string]bool)
	out := make([]pendingPlugin, 0, len(byName))

	var visit func(p pendingPlugin, chain []string) bool
	visit = func(p pendingPlugin, chain []string) bool {
		name := p.manifest.Name
		switch state[name] {
		case done:
			return !failed[name]
		case visiting:
			err := fmt.Errorf("dependency cycle: %s -> %s", strings.Join(chain, " -> "), name)
			problems = append(problems, err)
			rejected[name] = err
			return false
		}
		state[name] = visiting
		okAll := true
		for _, dep := range p.manifest.Requires {
			if dep == name {
				err := fmt.Errorf("plugin %q depends on itself", name)
				problems = append(problems, err)
				rejected[name] = err
				okAll = false
				continue
			}
			depP, inBatch := byName[dep]
			switch {
			case inBatch:
				if !visit(depP, append(chain, name)) {
					okAll = false
				}
			case loaded[dep]:
				// Satisfied by an already-registered plugin.
			default:
				err := fmt.Errorf("plugin %q requires %q which is not loaded", name, dep)
				problems = append(problems, err)
				rejected[name] = err
				okAll = false
			}
		}
		state[name] = done
		if !okAll {
			failed[name] = true
			if rejected[name] == nil {
				rejected[name] = fmt.Errorf("plugin %q has an unavailable dependency", name)
			}
			return false
		}
		out = append(out, p)
		return true
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		visit(byName[n], nil)
	}
	return out, problems, rejected
}

// Register persists a plugin manifest and loads it into memory.
func (m *Manager) Register(manifest *Manifest) error {
	if manifest == nil {
		err := errors.New("plugin manifest is nil")
		m.recordLoadFailure("<unknown>", err)
		return err
	}
	dir := m.pluginDirFor(manifest.Name)
	m.mu.RLock()
	root := m.pluginDir
	m.mu.RUnlock()
	if root == "" {
		root = filepath.Dir(dir)
	}
	err := m.registerAtDir(manifest, dir, root)
	if err != nil {
		m.recordLoadFailure(manifest.Name, err)
	}
	return err
}

// checkRegistration rejects duplicate names, self-dependencies and requires
// pointing at plugins that are not loaded. LoadFromDisk's topological pass
// already guarantees ordering for batch loads; this guards the direct path.
func (m *Manager) checkRegistration(manifest *Manifest) error {
	m.mu.RLock()
	prev, dup := m.plugins[manifest.Name]
	loaded := make(map[string]bool, len(m.plugins))
	for name := range m.plugins {
		loaded[name] = true
	}
	m.mu.RUnlock()
	if dup {
		return fmt.Errorf("plugin %q already registered (version %s); Unregister it first to replace",
			manifest.Name, prev.Version())
	}
	for _, dep := range manifest.Requires {
		if dep == manifest.Name {
			return fmt.Errorf("plugin %q depends on itself", manifest.Name)
		}
		if !loaded[dep] {
			return fmt.Errorf("plugin %q requires %q which is not loaded", manifest.Name, dep)
		}
	}
	return nil
}

func (m *Manager) registerAtDir(manifest *Manifest, pluginDir, root string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	// Package trust: a digest mismatch or a signature that no trusted key
	// vouches for is fatal before the plugin becomes callable.
	if err := m.VerifyPackage(pluginDir, root, manifest); err != nil {
		return err
	}
	entryPath := filepath.Join(pluginDir, manifest.Entry)
	if isWasmInterpreter(manifest.Interpreter) {
		// A WASM plugin has no interpreter to look up, but a missing module is
		// a hard error: it can never run.
		if _, err := os.Stat(entryPath); err != nil {
			return fmt.Errorf("wasm module not found: %s", manifest.Entry)
		}
	} else if _, err := exec.LookPath(manifest.Interpreter); err != nil {
		slog.Warn("Plugin interpreter not found on PATH; plugin will be registered but may fail at runtime",
			"plugin", manifest.Name, "interpreter", manifest.Interpreter, "error", err)
	}
	if _, err := os.Stat(entryPath); os.IsNotExist(err) {
		slog.Warn("Plugin entry file not found; plugin will be registered but may fail at runtime",
			"plugin", manifest.Name, "entry", manifest.Entry)
	}
	if err := m.checkRegistration(manifest); err != nil {
		return err
	}

	if err := manifest.Save(pluginDir); err != nil {
		return err
	}

	// Persist to DB.
	var record db.Plugin
	result := m.db.Where("name = ?", manifest.Name).First(&record)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}

	record.Name = manifest.Name
	record.Version = manifest.Version
	record.Description = manifest.Description
	record.Author = manifest.Author
	record.Type = manifest.Type
	record.Category = manifest.Category
	record.Config = manifest.ConfigJSON()
	if record.ID == 0 {
		record.Enabled = true
	}

	if err := m.db.Save(&record).Error; err != nil {
		return fmt.Errorf("failed to save plugin record: %w", err)
	}

	config := manifest.Config
	if record.Config != "" {
		var dbConfig map[string]interface{}
		if err := json.Unmarshal([]byte(record.Config), &dbConfig); err == nil {
			config = mergeMaps(config, dbConfig)
		}
	}

	p := &externalPlugin{
		manifest: manifest,
		dir:      pluginDir,
		exec:     m.exec,
	}
	p.enabled.Store(record.Enabled)
	if err := p.Init(context.Background(), config); err != nil {
		return fmt.Errorf("failed to initialise plugin %q: %w", manifest.Name, err)
	}

	m.mu.Lock()
	m.plugins[manifest.Name] = p
	m.mu.Unlock()
	m.recordLoadSuccess(manifest.Name)

	slog.Info("Plugin registered", "name", manifest.Name, "type", manifest.Type, "version", manifest.Version, "dir", pluginDir)
	return nil
}

// Unregister removes a plugin from memory and the database.
func (m *Manager) Unregister(name string) error {
	m.mu.Lock()
	pluginDir := ""
	if p, ok := m.plugins[name]; ok {
		// The plugin is being torn down either way; surface a slow/hung
		// shutdown instead of dropping it on the floor.
		if err := p.Shutdown(); err != nil {
			slog.Warn("plugin shutdown error during unregister", "plugin", name, "err", err)
		}
		if ep, ok := p.(*externalPlugin); ok {
			pluginDir = ep.dir
		}
	}
	delete(m.plugins, name)
	m.mu.Unlock()

	if err := m.db.Where("name = ?", name).Delete(&db.Plugin{}).Error; err != nil {
		return err
	}

	if pluginDir == "" {
		pluginDir = m.pluginDirFor(name)
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		slog.Warn("Failed to remove plugin directory", "dir", pluginDir, "err", err)
	}
	return nil
}

// Get returns a loaded plugin by name.
func (m *Manager) Get(name string) (Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	return p, nil
}

// List returns all loaded plugins.
func (m *Manager) List() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		list = append(list, p)
	}
	return list
}

// SetEnabled updates whether a plugin is allowed to execute.
func (m *Manager) SetEnabled(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var record db.Plugin
	if err := m.db.Where("name = ?", name).First(&record).Error; err != nil {
		return err
	}
	record.Enabled = enabled
	if err := m.db.Save(&record).Error; err != nil {
		return err
	}

	if p, ok := m.plugins[name]; ok {
		if ep, ok := p.(*externalPlugin); ok {
			ep.enabled.Store(enabled)
		}
	}
	return nil
}

// ExecuteCommand runs a command plugin with the given agent and parameters.
func (m *Manager) ExecuteCommand(ctx context.Context, name, agentID string, params map[string]interface{}) (*Result, error) {
	p, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if !p.IsEnabled() {
		return nil, fmt.Errorf("plugin %q is disabled", name)
	}
	cp, ok := p.(CommandPlugin)
	if !ok {
		return nil, fmt.Errorf("plugin %q is not a command plugin", name)
	}
	return cp.Execute(ctx, agentID, params)
}

// ExecuteHook runs all hook plugins subscribed to the given event.
func (m *Manager) ExecuteHook(ctx context.Context, event Event) error {
	m.mu.RLock()
	plugins := make([]Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	errCh := make(chan error, len(plugins))
	for _, p := range plugins {
		hp, ok := p.(HookPlugin)
		if !ok || !p.IsEnabled() {
			continue
		}
		if !contains(hp.SubscribedEvents(), event.Type) {
			continue
		}
		if !m.hookAllowed(hp.Name()) {
			// Breaker tripped: a persistently failing plugin (broken
			// interpreter, missing runtime) must not fork+log on every event.
			slog.Debug("Hook plugin skipped by breaker", "plugin", hp.Name(), "event", event.Type)
			continue
		}
		wg.Add(1)
		go func(h HookPlugin) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					// Isolate panicking hook plugins: ExecuteHook is called
					// from request paths and its wg.Wait would otherwise
					// propagate the panic and crash the process.
					slog.Error("Panic in hook plugin", "plugin", h.Name(), "event", event.Type, "recover", r)
				}
			}()
			hookErr := h.OnEvent(ctx, event)
			m.hookRecord(h.Name(), hookErr)
			if hookErr != nil {
				slog.Warn("Hook plugin execution failed", "plugin", h.Name(), "event", event.Type, "err", hookErr)
				errCh <- hookErr
			}
		}(hp)
	}
	wg.Wait()
	close(errCh)

	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("hook execution errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// GenerateReport runs a report plugin with the given parameters.
func (m *Manager) GenerateReport(ctx context.Context, name string, params map[string]interface{}) (*Report, error) {
	p, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if !p.IsEnabled() {
		return nil, fmt.Errorf("plugin %q is disabled", name)
	}
	rp, ok := p.(ReportPlugin)
	if !ok {
		return nil, fmt.Errorf("plugin %q is not a report plugin", name)
	}
	return rp.Generate(ctx, params)
}

// PluginDir returns the on-disk directory used for the named plugin.
func (m *Manager) PluginDir(name string) string {
	return m.pluginDirFor(name)
}

func (m *Manager) pluginDirFor(name string) string {
	safe := strings.ReplaceAll(name, "..", "_")
	safe = strings.ReplaceAll(safe, string(filepath.Separator), "_")
	safe = strings.ReplaceAll(safe, "/", "_")
	return filepath.Join(m.pluginDir, safe)
}

// externalPlugin adapts a Manifest into the Plugin interfaces.
type externalPlugin struct {
	manifest *Manifest
	dir      string
	enabled  atomic.Bool
	config   map[string]interface{}
	exec     *executor
}

func (p *externalPlugin) Name() string        { return p.manifest.Name }
func (p *externalPlugin) Version() string     { return p.manifest.Version }
func (p *externalPlugin) Type() string        { return p.manifest.Type }
func (p *externalPlugin) Description() string { return p.manifest.Description }
func (p *externalPlugin) IsEnabled() bool     { return p.enabled.Load() }
func (p *externalPlugin) Manifest() *Manifest { return p.manifest }

func (p *externalPlugin) Init(ctx context.Context, config map[string]interface{}) error {
	p.config = config
	if p.config == nil {
		p.config = make(map[string]interface{})
	}
	return nil
}

func (p *externalPlugin) Shutdown() error { return nil }

func (p *externalPlugin) Execute(ctx context.Context, agentID string, params map[string]interface{}) (*Result, error) {
	input := map[string]interface{}{
		"agent_id": agentID,
		"params":   params,
		"config":   p.config,
	}
	res, err := p.exec.run(ctx, p.dir, p.manifest, input, p.manifest.Timeout)
	if err != nil {
		return nil, err
	}
	r, parseErr := parseResult(res.Stdout)
	if parseErr != nil {
		return &Result{
			Success: false,
			Output:  string(res.Stdout),
			Error:   parseErr.Error(),
		}, nil
	}
	return r, nil
}

func (p *externalPlugin) OnEvent(ctx context.Context, event Event) error {
	input := map[string]interface{}{
		"event":  event,
		"config": p.config,
	}
	res, err := p.exec.run(ctx, p.dir, p.manifest, input, p.manifest.Timeout)
	if err != nil {
		return err
	}
	if len(res.Stdout) == 0 {
		return nil
	}
	_, err = parseResult(res.Stdout)
	return err
}

func (p *externalPlugin) SubscribedEvents() []string { return p.manifest.Events }

func (p *externalPlugin) Generate(ctx context.Context, params map[string]interface{}) (*Report, error) {
	input := map[string]interface{}{
		"params": params,
		"config": p.config,
	}
	res, err := p.exec.run(ctx, p.dir, p.manifest, input, p.manifest.Timeout)
	if err != nil {
		return nil, err
	}
	return parseReport(res.Stdout)
}

func contains(slice []string, value string) bool {
	for _, s := range slice {
		if s == value {
			return true
		}
	}
	return false
}

func mergeMaps(base, override map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}
