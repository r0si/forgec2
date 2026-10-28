package scripting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/console"
	"github.com/dop251/goja_nodejs/require"
)

// scriptSourceLimit caps stored/executed script source. A script lives in the
// server process, so an unbounded body is both a memory and a parse-time
// denial-of-service vector.
const scriptSourceLimit = 1 << 20 // 1 MiB

// scriptParseTimeout bounds the top-level pass that collects a script's event
// subscriptions. Parsing and registration are cheap; anything slower is a
// runaway script.
const scriptParseTimeout = 10 * time.Second

// validateScriptSource enforces the source cap for every entry point that
// accepts operator-supplied JavaScript.
func validateScriptSource(code string) error {
	if len(code) > scriptSourceLimit {
		return fmt.Errorf("script too large: %d bytes (max %d)", len(code), scriptSourceLimit)
	}
	return nil
}

// errHostModulesDisabled is returned by the script module loader for every
// file-backed require().
var errHostModulesDisabled = errors.New("require() of host modules is disabled in ForgeC2 scripts")

// newScriptRegistry builds the require() registry used by script VMs.
// console/util are Go-registered core modules and still resolve; anything
// that would read a file from the host is refused, so a script cannot load or
// evaluate server-side JS/JSON or walk out of the capability bridge. Scripts
// get console plus the explicit bridge API and nothing else.
func newScriptRegistry() *require.Registry {
	return require.NewRegistry(require.WithLoader(func(string) ([]byte, error) {
		return nil, errHostModulesDisabled
	}))
}

// Caller identifies who triggered a script execution. The bridge uses it to
// enforce permissions: admin callers get full access, everyone else is
// limited to the permissions of their role.
type Caller struct {
	Username string
	Role     string
	// TenantID scopes data access: 0 is the legacy global view, non-zero
	// restricts bridge reads/writes to that tenant's rows.
	TenantID uint
}

// Bridge is the server-side capability layer scripts call through. Every
// method is synchronous: results are returned to the script, errors are
// thrown as JavaScript exceptions. Implementations must enforce permissions
// via Caller before touching the database or the network.
type Bridge interface {
	SendTask(caller Caller, agentID, taskType, params string) (uint64, error)
	GetAgent(caller Caller, agentID string) (map[string]interface{}, error)
	ListAgents(caller Caller) ([]map[string]interface{}, error)
	HTTPRequest(caller Caller, method, url string, headers map[string]string, body string, timeoutSecs int) (map[string]interface{}, error)
	Query(caller Caller, kind string, args map[string]interface{}) (interface{}, error)
}

type ScriptEngine struct {
	vm      *goja.Runtime
	mu      sync.Mutex
	bridge  Bridge
	scripts []*LoadedScript
}

type LoadedScript struct {
	ID     uint
	Name   string
	Code   string
	Events []string
	Active bool
}

var globalEngine *ScriptEngine

func init() {
	globalEngine = NewScriptEngine()
}

func NewScriptEngine() *ScriptEngine {
	vm := goja.New()
	// goja ships maxCallStackSize = math.MaxInt32, i.e. NO depth limit: an unbounded-recursion
	// script overflows the GO stack, which is a fatal runtime error that recover() cannot catch -
	// the whole server dies. Scripts can also be bound to events, so a saved one would crash the
	// process on every matching event. This makes deep recursion a catchable JS error instead.
	vm.SetMaxCallStackSize(scriptMaxCallStackSize)
	newScriptRegistry().Enable(vm)
	console.Enable(vm)

	e := &ScriptEngine{
		vm: vm,
	}
	// Dormant template VM (no e.vm call sites): keep its captured caller at
	// the lowest privilege so a future wiring cannot inherit admin reach.
	e.registerAPI(vm, Caller{Username: "system", Role: "user"}, newExecBudget())
	return e
}

// execBudget counts bridge calls in ONE execution, split by side.
//
// Every sendTask call inserts a task row and an audit row, and the database has a single writer, so
// a script that loops over sendTask - or an event-bound script firing on every matching event - was
// a write-amplification primitive. Reads are cheaper but still pin that one connection and allocate
// result sets, so they get their own, larger bound.
type execBudget struct {
	mu    sync.Mutex
	wrote int
	read  int
}

func newExecBudget() *execBudget { return &execBudget{} }

// spendWrite bounds the calls that create rows (task + audit per sendTask).
func (b *execBudget) spendWrite() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wrote >= scriptMaxBridgeMutations {
		return fmt.Errorf("script exceeded the mutating bridge call limit (%d); send fewer tasks per execution", scriptMaxBridgeMutations)
	}
	b.wrote++
	return nil
}

// spendRead bounds the read side (query and sleep).
func (b *execBudget) spendRead() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.read >= scriptMaxBridgeQueries {
		return fmt.Errorf("script exceeded the bridge query limit (%d); narrow the query or run it once", scriptMaxBridgeQueries)
	}
	b.read++
	return nil
}

func GetEngine() *ScriptEngine {
	return globalEngine
}

// SetBridge installs the server-side capability layer. Called once at server
// startup before any script can execute.
func (e *ScriptEngine) SetBridge(b Bridge) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bridge = b
}

func (e *ScriptEngine) bridgeFor(caller Caller) Bridge {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bridge
}

// setAPIOn installs one scripting-API binding on the VM. goja only rejects it
// when the VM is in an unreachable state, in which case the script will hit a
// ReferenceError on that name — surface it instead of dropping silently.
func setAPIOn(vm *goja.Runtime, name string, val any) {
	if err := vm.Set(name, val); err != nil {
		slog.Error("scripting: failed to register API binding", "name", name, "err", err)
	}
}

// registerAPI installs the JavaScript API surface on the given VM. The bridge
// calls are synchronous: on failure the script sees a thrown exception.
func (e *ScriptEngine) registerAPI(vm *goja.Runtime, caller Caller, budget *execBudget) {
	setAPIOn(vm, "sendTask", func(call goja.FunctionCall) goja.Value {
		bridge := e.bridgeFor(caller)
		if bridge == nil {
			panic(vm.NewGoError(errors.New("script bridge is not available")))
		}
		// See execBudget: this is the mutating call that a loop turns into table growth.
		if err := budget.spendWrite(); err != nil {
			panic(vm.NewGoError(err))
		}
		agentID := call.Argument(0).String()
		taskType := call.Argument(1).String()
		params := call.Argument(2).String()
		id, err := bridge.SendTask(caller, agentID, taskType, params)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(id)
	})

	setAPIOn(vm, "getAgent", func(call goja.FunctionCall) goja.Value {
		bridge := e.bridgeFor(caller)
		if bridge == nil {
			panic(vm.NewGoError(errors.New("script bridge is not available")))
		}
		agentID := call.Argument(0).String()
		agent, err := bridge.GetAgent(caller, agentID)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(agent)
	})

	setAPIOn(vm, "listAgents", func(call goja.FunctionCall) goja.Value {
		bridge := e.bridgeFor(caller)
		if bridge == nil {
			panic(vm.NewGoError(errors.New("script bridge is not available")))
		}
		agents, err := bridge.ListAgents(caller)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(agents)
	})

	setAPIOn(vm, "httpRequest", func(call goja.FunctionCall) goja.Value {
		bridge := e.bridgeFor(caller)
		if bridge == nil {
			panic(vm.NewGoError(errors.New("script bridge is not available")))
		}
		rawURL := call.Argument(0).String()
		opts := map[string]interface{}{}
		if obj, ok := call.Argument(1).(*goja.Object); ok {
			for _, k := range obj.Keys() {
				opts[k] = obj.Get(k).Export()
			}
		}
		method := "GET"
		if m, ok := opts["method"].(string); ok && m != "" {
			method = m
		}
		headers := map[string]string{}
		if h, ok := opts["headers"].(map[string]interface{}); ok {
			for k, v := range h {
				headers[k] = fmt.Sprintf("%v", v)
			}
		}
		body := ""
		if b, ok := opts["body"].(string); ok {
			body = b
		}
		timeout := 10
		if t, ok := opts["timeout"]; ok {
			if n, ok := toInt64(t); ok && n > 0 {
				timeout = int(n)
			}
		}
		res, err := bridge.HTTPRequest(caller, method, rawURL, headers, body, timeout)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(res)
	})

	setAPIOn(vm, "query", func(call goja.FunctionCall) goja.Value {
		bridge := e.bridgeFor(caller)
		if bridge == nil {
			panic(vm.NewGoError(errors.New("script bridge is not available")))
		}
		kind := call.Argument(0).String()
		args := map[string]interface{}{}
		if obj, ok := call.Argument(1).(*goja.Object); ok {
			for _, k := range obj.Keys() {
				args[k] = obj.Get(k).Export()
			}
		}
		if err := budget.spendRead(); err != nil {
			panic(vm.NewGoError(err))
		}
		result, err := bridge.Query(caller, kind, args)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(result)
	})

	setAPIOn(vm, "log", func(call goja.FunctionCall) goja.Value {
		msg := call.Argument(0).String()
		slog.Info("script", "message", msg)
		return goja.Undefined()
	})

	setAPIOn(vm, "sleep", func(call goja.FunctionCall) goja.Value {
		// Capped and budgeted: a Go-side sleep cannot be interrupted, so a 60s sleep kept the VM
		// goroutine alive long after the caller had been told the script timed out.
		ms := call.Argument(0).ToInteger()
		if ms > scriptMaxSleepMillis {
			ms = scriptMaxSleepMillis
		}
		if ms > 0 {
			if err := budget.spendRead(); err != nil {
				panic(vm.NewGoError(err))
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		return goja.Undefined()
	})
}

func toInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

func (e *ScriptEngine) LoadScript(id uint, name, code string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, s := range e.scripts {
		if s.Name == name {
			s.Code = code
			return e.reparseEvents(s)
		}
	}

	ls := &LoadedScript{
		ID:     id,
		Name:   name,
		Code:   code,
		Active: true,
	}
	if err := e.reparseEvents(ls); err != nil {
		return err
	}
	e.scripts = append(e.scripts, ls)
	return nil
}

func (e *ScriptEngine) reparseEvents(s *LoadedScript) error {
	if err := validateScriptSource(s.Code); err != nil {
		return err
	}
	// Enable the same surface executeScript installs. A bare VM made load-time
	// validation stricter than the runtime: a script that calls console.log (or
	// require("console")) parsed fine when executed but was REJECTED here with
	// "ReferenceError: console is not defined", so it could never be saved.
	vm := goja.New()
	// goja ships maxCallStackSize = math.MaxInt32, i.e. NO depth limit: an unbounded-recursion
	// script overflows the GO stack, which is a fatal runtime error that recover() cannot catch -
	// the whole server dies. Scripts can also be bound to events, so a saved one would crash the
	// process on every matching event. This makes deep recursion a catchable JS error instead.
	vm.SetMaxCallStackSize(scriptMaxCallStackSize)
	newScriptRegistry().Enable(vm)
	console.Enable(vm)
	events := make(map[string]bool)
	setAPIOn(vm, "on", func(call goja.FunctionCall) goja.Value {
		eventType := call.Argument(0).String()
		events[eventType] = true
		return goja.Undefined()
	})
	// Compile only - never run the script here. Running it made "save the
	// script" execute the whole script body: a top-level sendTask() fired a real
	// task against an agent, sleep() blocked the request, and any reference to a
	// context binding (agent_id, args, ...) failed with ReferenceError because the
	// per-run bindings do not exist at load time. Compiling still reports syntax
	// errors, which is all this check is for.
	done := make(chan error, 1)
	go func() {
		_, err := goja.Compile("", s.Code, false)
		done <- err
	}()
	timer := time.NewTimer(scriptParseTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("script parse error: %w", err)
		}
	case <-timer.C:
		vm.Interrupt("script parse timeout")
		<-done
		return fmt.Errorf("script parse timeout (%s)", scriptParseTimeout)
	}
	s.Events = make([]string, 0, len(events))
	for evt := range events {
		s.Events = append(s.Events, evt)
	}
	return nil
}

func (e *ScriptEngine) UnloadScript(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, s := range e.scripts {
		if s.Name == name {
			e.scripts = append(e.scripts[:i], e.scripts[i+1:]...)
			return
		}
	}
}

// FireEvent and runEventCallback were removed along with the on/off
// registration API. Nothing in the tree ever fired an event, so every callback
// registered via on() was unreachable: the events map, the exec mutex that
// serialized dispatch, the per-callback timeout and the JSON data marshalling
// all existed to serve a call that never happened. Scripts keep the rest of
// the bridge (sendTask/getAgent/listAgents/httpRequest/query/sleep).

type Script struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Code        string    `json:"code"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	RunCount    int       `json:"run_count"`
	LastRun     time.Time `json:"last_run"`
}

type ExecutionResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error"`
}

func (e *ScriptEngine) Execute(scriptID string, context map[string]interface{}, caller Caller) ExecutionResult {
	// Snapshot the code under the lock and run WITHOUT holding it:
	// executeScript installs an on() API whose callback re-locks e.mu, so
	// running under the lock deadlocked every hook-declaring script until
	// the 30s timeout fired — while the zombie VM then resumed detached.
	e.mu.Lock()
	var code string
	found := false
	for _, s := range e.scripts {
		if fmt.Sprintf("%d", s.ID) == scriptID || s.Name == scriptID {
			code = s.Code
			found = true
			break
		}
	}
	e.mu.Unlock()
	if !found {
		return ExecutionResult{Success: false, Error: "script not found"}
	}
	return e.executeScript(code, context, caller)
}

func (e *ScriptEngine) ExecuteCode(code string, context map[string]interface{}, caller Caller) ExecutionResult {
	return e.executeScript(code, context, caller)
}

func (e *ScriptEngine) executeScript(code string, scriptCtx map[string]interface{}, caller Caller) ExecutionResult {
	if err := validateScriptSource(code); err != nil {
		return ExecutionResult{Success: false, Error: err.Error()}
	}
	vm := goja.New()
	// goja ships maxCallStackSize = math.MaxInt32, i.e. NO depth limit: an unbounded-recursion
	// script overflows the GO stack, which is a fatal runtime error that recover() cannot catch -
	// the whole server dies. Scripts can also be bound to events, so a saved one would crash the
	// process on every matching event. This makes deep recursion a catchable JS error instead.
	vm.SetMaxCallStackSize(scriptMaxCallStackSize)
	newScriptRegistry().Enable(vm)
	console.Enable(vm)

	for k, v := range scriptCtx {
		setAPIOn(vm, k, v)
	}

	budget := newExecBudget()
	e.registerAPI(vm, caller, budget)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan ExecutionResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				select {
				case done <- ExecutionResult{Success: false, Error: fmt.Sprintf("panic: %v", r)}:
				default:
				}
			}
		}()
		v, err := vm.RunString(code)
		if err != nil {
			select {
			case done <- ExecutionResult{Success: false, Error: err.Error()}:
			default:
			}
			return
		}
		if v != nil {
			// Cap the rendered result: a script can return an arbitrarily large string, and it
			// travels straight into an HTTP response.
			output := v.String()
			if len(output) > scriptMaxOutputBytes {
				output = output[:scriptMaxOutputBytes] + "\n[output truncated]"
			}
			select {
			case done <- ExecutionResult{Success: true, Output: output}:
			default:
			}
		} else {
			select {
			case done <- ExecutionResult{Success: true, Output: ""}:
			default:
			}
		}
	}()

	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		// goja is only preemptible via Interrupt: without this, an infinite
		// loop script keeps a 100%-CPU goroutine spinning forever (and keeps
		// firing bridge side effects after the caller already saw failure).
		vm.Interrupt("script execution timeout (30s)")
		return ExecutionResult{Success: false, Error: "script execution timeout (30s)"}
	}
}

// Limits for a scripted execution. See executeScript for why the stack bound is load-bearing.
const (
	scriptMaxCallStackSize = 4096
	scriptMaxOutputBytes   = 1 << 20
	// scriptMaxBridgeMutations bounds one execution's writes (task + audit row per sendTask).
	scriptMaxBridgeMutations = 500
	// scriptMaxBridgeQueries bounds read-side calls (query and sleep) per execution.
	scriptMaxBridgeQueries = 5000
	// scriptMaxSleepMillis caps one sleep. The execution timeout is 30s and a Go sleep cannot be
	// interrupted, so a 60s sleep kept the VM goroutine alive long after the caller was told the
	// script timed out.
	scriptMaxSleepMillis = 5000
)
