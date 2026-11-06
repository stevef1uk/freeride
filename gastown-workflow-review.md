# Gastown workflow resilience review

Review date: 6 November 2026. Source checkout: gastown commit `16b0538a`.

## Scope and evidence

Reviewed the workflow template, orchestrated agent loop, recovery and progress tracking, phase generation and advancement, stuck monitor, language detection, verification paths, process cleanup, and FreeRide paid-model selection. Read the GitHub SPEC.md files for stevef1uk/testgt3 and testgt4, and the testgt4 profile supplied from omen. Neither remote run logs nor omen's binary/template versions were available. The local town has no registered rigs; the checked-in rigs.json lists testgt1. Consequently this is a source and configuration review, not a completed forensic reconstruction of either remote run. No application code, configuration, credentials, or remote rigs were changed. No paid model runs were initiated.

## Assessment

The concern about Phase 1 is well founded. The supplied project-foundation phase has 33 files, including components for capabilities scheduled for later phases. The workflow operates sequentially and requests a claim, implementation, verification where applicable, and close for each bead. At three separate model turns per file, 33 files consume 99 turns before much repair or final completion work. Commands can be combined, so this is a sizing illustration rather than an exact minimum.

There are additional independent defects. Increasing the 100-turn setting alone would leave replay, weak acceptance checks, destructive recovery heuristics, and unbounded paid retries unresolved. The target should be durable incremental completion with bounded recovery costs.

## Priority findings

### 1. Explicit milestones bypass phase-size safeguards — high

`internal/orchestrator/delivery_phase.go:FinalizeDeliveryPhases` returns immediately for ExplicitDeliveryContract. The supplied profile sets that flag. The later splitOverlargePhases call therefore cannot protect its 33-file foundation phase. Even the non-explicit path uses maxFilesPerPhase=20 while its comment recommends no more than 10 at approximately 10 calls per file.

Preserve the SPEC's eight business milestones, but execute each through smaller dependency-aware batches. Batch acceptance and milestone acceptance must be separate: copying the milestone's full Docker/dashboard gate onto every partial file chunk creates impossible intermediate gates. Estimate turns, context and verification time; split work before dispatch, with reserve for repair. File count can be a fallback heuristic, not the primary capacity model.

### 2. Attempt exhaustion can invalidate completed work — high

`cmd/gt-agent/orchestrated.go` runs the timeout hook after exhausting model turns, then calls ReopenAllPhaseImplementBeads. `internal/orchestrator/implementation_recovery.go` attempts to reopen closed beads and clears implementation-progress.json. The open-bead circuit-breaker path also calls the reopening function.

There is a second bug: reopening tests whether bead titles contain the phase ID, although normal titles identify paths. That can reopen nothing or match unrelated title text. Thus the precise amount of replay requires the actual bead titles and logs; a complete replay is not established from source alone.

Make a session limit a checkpoint/yield operation. Preserve verified closed beads, durable attempt counts and the next bead. Reopen only tasks whose recorded verification is invalidated by changed inputs or explicit failing evidence. Use structured phase/task identifiers, never substring matching on titles.

### 3. Per-bead loop breaker cannot reach its stated threshold — high

`cmd/gt-agent/implementation_progress.go:addBeadTurn` defaults to a 20-turn limit. Above that limit, a file of at least 400 bytes is preserved and its counter is reset to 20. The next call increments to 21 and resets again. The intended failure at 40 turns is unreachable through ordinary consecutive calls. Files below 400 bytes may instead be deleted, even when a short initializer or configuration file is valid.

Keep monotonic total attempts separate from consecutive attempts without verified progress. Do not delete code based on size. Preserve snapshots, classify the failure, then create a bounded repair or smaller task. Test 45 consecutive calls against an unchanged substantive file and verify the breaker fires.

### 4. Paid access has no workflow spending boundary in the reviewed path — high

`models.yaml` places polecat, planner, architect, tester and setup in rolePrependBeforeOriginal and lists a paid OpenRouter model first for each. `main.go:selectCandidatesBase` consults these lists when allowPaid is enabled; earlier provider tiers can still affect the actual selected model. The flag permits paid-first routing, not merely an emergency paid fallback.

`internal/llm/client.go:CompletionResponse` captures model and choices, but not token usage or cost. The reviewed workflow loop has no aggregate dollar/token/attempt budget across restarts. Existing cost tiers and review retry counters are not monetary enforcement.

Add per-workflow and per-rig budgets, with a shared proxy-side admission check covering every fallback and retry. Reserve estimated maximum request cost before dispatch and reconcile actual usage afterward. Persist the budget across process restarts, and stop admitting calls when accounting cannot establish remaining capacity. Record workflow, phase, task, attempt, requested/actual model, provider request ID, tokens and cost together. Allow-paid should remain an explicit routing permission distinct from a spending allowance.

### 5. Billing/authentication failures are retried as temporary outages — high

`cmd/gt-agent/implementation_guard.go:isRetriableLLMError` groups 402, 403, credits, billing and quota text with temporary provider failures. The outer orchestrated loop sleeps 90 seconds and tries again without completing the task. Additional retries exist in completeMessagesWithRetry and the LLM client, plus proxy fallback. These layers lack a shared attempt boundary.

Classify structured errors: permanent authentication/configuration failures; depleted budget/credits; transient rate limits; transient provider/network failures; and task defects. Use Retry-After and jitter for temporary failures. Depleted credits should enter a durable resource-blocked state with no model polling; resume when credentials/budget state changes. Keep transient retries bounded across all layers, not independently per layer.

### 6. Stuck detection measures state age rather than time since progress — high

`internal/orchestrator/workflow_stuck.go:EvalWorkflowStuck` compares the current bead fingerprint with the last sampled fingerprint, but measures elapsed time from StateEnteredAt. `workflow_stuck_monitor.go` updates the fingerprint each tick. A long-running phase can make progress and then be classified idle on its next unchanged sample because the phase itself is already older than the idle threshold. The fingerprint also excludes source edits and successful intermediate checks.

Persist last_verified_progress_at and compare against that timestamp. Track meaningful signals such as task completion, improved failing-test sets and accepted checkpoints. Repeated metadata rewrites should not count as progress. Give an active worker a lease; repair should coordinate cancellation or wait for the lease before mutating its queue. Test this with a fake clock and progress immediately before the idle deadline.

### 7. Recovery can terminate other rigs' servers — high

`internal/orchestrator/dev_server_ports.go:StopDevServersForRig` kills listeners by port and invokes global pkill patterns for uvicorn, gunicorn and hypercorn. The shell fallback in scripts/stop-rig-dev-servers.sh globally matches Go server processes. Two rigs using common ports or Python servers can interfere with each other's work.

Record owned process groups/container IDs at launch. Stop only resources with matching workflow/rig ownership. Use a distinct Compose project name and allocated ports per concurrent rig. Test two running rigs and prove recovery of one leaves the other healthy.

### 8. Production behaviour remains project-specific — high

Examples include the literal `Implement finally/` case in docker_workflow.go, linkshelf-specific file probes in plan_review_guard.go, testgt3/linkshelf ignore and untracking entries in internal/rig/overlay.go, a testgt3 fallback in the reconcile-implement-beads command, and a pingapp Python example emitted by the agent loop. The rig-flow YAML also tells frontend work to use plain JavaScript without module imports and allows immediate closure of JS/CSS/HTML beads. Those assumptions conflict with React/Next projects.

Remove executable project-name assumptions. Derive paths, static-serving rules, build requirements and generated-artifact exclusions from explicit project contracts and detected package roots. Keep named sample projects in test fixtures/documentation only. Rename fixture projects and move directories in regression tests to detect hidden coupling.

### 9. Language support is exclusive where it must be compositional — high

`internal/orchestrator/python_workflow.go:UsesPythonVenv` returns false when WorkflowUsesGo is true. That disables Python handling in a Go/Python project. Other inference relies on a single test runner, a layout root, directory names and command-string inspection.

Represent independently configured components: root, language/toolchain, install/build/test commands, environment, artifacts and dependencies. A rig can contain several Go modules, Python environments and Node packages plus a separate Playwright harness. Run setup and verification per component; integration gates connect components. Toolchain adapters should supply language knowledge without making the scheduler language-specific.

### 10. The supplied testgt4 profile is not a reliable acceptance contract — high

Specific issues in the supplied configuration:

- Foundation contains advanced frontend components, while market and portfolio stages repeatedly name the same small set of backend files. Shared files are legitimate, but a path list cannot distinguish creating a skeleton from implementing a later feature in that file.
- Several phase gates test route registration or compileall, not streaming, portfolio arithmetic, persistence or chat behaviour. They can pass incomplete implementations.
- The foundation HTML grep accepts any one of several alternatives; it does not prove heading, tickers and cash are all present. A client-rendered dashboard may also require browser assertions rather than raw HTML grep.
- Some embedded Python assertions contain nested double quotes requiring shell-level validation. The Python import cwd/environment must also be made explicit for backend/app.
- Build commands use --no-cache. Current source attempts to remove it during sanitisation, but the supplied saved profile still contains it. Verify omen's actual binary, template and effective command rather than assuming current source behaviour.
- Chained cleanup with `&& docker compose down` skips cleanup on preceding failure. Use guaranteed teardown while retaining the original test exit code.
- A global 400-byte implementation minimum is unsuitable for __init__.py and .gitkeep, and is not evidence of correctness for a large source file.
- `source: deterministic` and `confidence: high` are not proof of sound phase assignment. The current indexer can call an LLM for assignments and sets high confidence in its deterministic output path.

Validate profiles before spending on implementation: dependency completeness/cycles, task ownership, gate prerequisites, component environments, shell quoting, build scope and requirement coverage. Represent checks as structured command/argv/cwd/env/timeout/cleanup records or versioned test scripts instead of increasingly rewritten shell strings. Distinguish create and modify tasks for shared files by phase and requirement IDs.

### 11. Growing conversation and split persistence complicate recovery — medium

The reviewed orchestrated loop appends model responses and feedback to messages for the entire attempt. It has no visible conversation compaction at bead boundaries. This increases context and replay cost as a large phase proceeds. Manager instance persistence uses temp-file rename, a useful existing safeguard, but profile, bead state, progress and workflow transitions are separate updates. Phase advancement also treats some follow-up sync failures as warnings.

Use short task sessions with durable, bounded handoff records: accepted commit, active task, remaining queue, failed checks, dependency contracts and next action. Journal transitions and reconcile interrupted multi-store operations idempotently. Add attempt/lease generations to reject stale completions. A crash after a bead closes but before state persistence must not replay verified work or advance without its gate.

## Recommended implementation order

1. Preserve completed beads on yield; fix monotonic counters; classify permanent API failures; enforce durable cost/attempt limits.
2. Introduce bounded execution batches beneath explicit milestones; preflight generated profiles before dispatch.
3. Correct progress timestamps and resource ownership so the monitor cannot fight healthy workers or other rigs.
4. Replace project-specific and single-language assumptions with component contracts and toolchain adapters.
5. Make acceptance evidence durable, strengthen behavioural gates, and add crash/restart reconciliation.

## Verification programme without paid API calls

Use scripted model responses, fake clocks, temporary repositories and a fake proxy with a synthetic usage ledger. Required scenarios:

- A 33-task explicit milestone spans several sessions and completes each accepted task once.
- Exhaustion at turn 100 preserves verified work and resumes the next task after process restart.
- An unchanged 400+ byte file reaches the bead breaker; a valid short file is never deleted by size.
- A repeated identical failing test causes escalation or decomposition within a durable attempt limit.
- 402/403 halt model dispatch; 429 respects backoff; transient failures consume one shared retry allowance.
- Budget accounting covers fallback attempts and concurrent rigs and survives a proxy restart.
- Progress just before the idle deadline postpones repair; harmless metadata edits do not.
- Recovering one rig cannot stop the other's server/container.
- Go-only, Python-only, Node-only and Go/Python/Node mixed fixtures complete equivalent acceptance contracts.
- Renaming all fixture roots and rig names does not change orchestration behaviour.
- A placeholder dashboard, route-only API and empty test suite all fail acceptance.
- Crash injection around task closure, checkpoint persistence and phase advancement resumes consistently.

Passing these establishes scheduler/recovery properties, not guaranteed LLM coding success on arbitrary projects. A later bounded real-model run would assess model capability separately, only using an explicitly available service and allowance.

## Remaining forensic evidence

To establish exactly what exhausted the omen credits, correlate its run logs, instances.json, bead status/history, effective template, proxy model/cost history and installed build revision. The supplied profile supports the oversized-phase diagnosis, but does not establish the precise failing commands, selected models, number of replays, or dollars attributable to each path. The second rig's effective profile was not supplied; its successful SPEC is a useful contrasting fixture, not a substitute for that configuration.

## Validation performed

The existing `TestEvalWorkflowStuck_*` tests passed locally with a temporary Go build cache. Selected gt-agent completion/command-timeout/error tests also passed using `-run 'Test(ImplementationCompletion|Completion|IsRetriable|StateRunner_effectiveCommandTimeoutSec|IsCmdTimeoutError)'`. These cover current expected behaviour; they do not establish that the defects above are fixed. A broad three-package test run was stopped without a result and must not be reported as passing. No real-model execution or remote-run reproduction was performed.

Reference specifications: [testgt3](https://github.com/stevef1uk/testgt3/blob/main/SPEC.md) and [testgt4](https://github.com/stevef1uk/testgt4/blob/main/SPEC.md).

## Implementation follow-through

The subsequent patch implements non-destructive session yields with eight-bead
execution batches, durable attempt limits, preserved checkpoint metadata,
workflow-scoped request/token admission and proxy-side fallback admission. The
proxy also reserves an OpenRouter text-inference dollar allowance (default $5
per workflow/$20 shared per rig) and submits corresponding provider price caps.
Reservations survive restarts and are not automatically refunded. This policy
bounds this proxy's supported requests; it is not an external billing guarantee.

Additional fixes cover permanent billing/authentication failures, Retry-After,
usage/identity logging, last-progress timestamps and source fingerprints,
rig-owned listener cleanup, shared-path phase ownership, independent component
setup, mixed Go/Python detection, phase dependency validation, and executable
project-name assumptions identified in this review. Python traceback ownership
now canonicalizes symlinks, fixing /var versus /private/var recovery on macOS.

The actual 33-file remote milestone and its acceptance criteria are preserved;
bounded sessions execute beneath the milestone rather than mechanically splitting
its Docker gate across incomplete file chunks. The design prompt now requires
phase-specific capabilities and behavioural evidence.

Validation: FreeRide's full local suite passed. The five gastown packages
(orchestrator, specprofile, llm, rig, gt-agent) passed with system PATH, so installed
Beads/Dolt integration is excluded, and three macOS shell tests are omitted.
Those three failures were reproduced against an unchanged archive of the original
commit: TestDesignReworkRequiresCurrentAmendment,
TestSelfManagedPythonSmokePreservesStatusAndLogs and
TestArchitectShellRepairProducesValidDeliveryContract. New tests cover 33-bead
batch accounting, durable counters, short-file preservation, concurrent admission,
corrupt ledgers, shared rig allowances, provider caps, price reservations, retry
admission, metadata preservation, mixed component roots and two-rig cleanup.
No real paid model execution or omen deployment was performed.

Remaining architectural work: versioned structured acceptance commands,
transactional multi-store/worker-lease migration and full independent component
verification/environment adapters. These are not claimed complete by the patch.
Operational instructions and compatibility limits are documented in
`gastown/docs/design/workflow-resilience.md` and the FreeRide README.
