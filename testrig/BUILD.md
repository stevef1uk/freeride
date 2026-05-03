# GasTown Build Workflow for Freeride

This directory contains the automated build system for the freeride project, integrated with GasTown's multi-agent orchestration.

## Architecture

```
GasTown (gt)
├── Mayor (coordination)
├── Deacon (health monitoring)
├── Witness (testrig)
│   └── Polecats
│       └── builder (build automation)
└── testrig/
    ├── build.sh (main build script)
    ├── gt-build-manager.sh (build controller)
    └── gt-build-trigger.sh (automated trigger)
```

## Quick Start

### 1. Check Build Status
```bash
cd /home/stevef/dev/freeride/testrig
./gt-build-manager.sh status
```

### 2. Start a Build
```bash
# Manual build
./build.sh all

# Or via GasTown manager
./gt-build-manager.sh start

# Watch logs
./gt-build-manager.sh logs
```

### 3. Assign Build Work to Polecat
```bash
# Create build task
cd /home/stevef/dev/freeride/testrig
bd create --title="Run full build" --type=task

# Or via GasTown
gt assign "Run full build" testrig --type=task
```

## Build Targets

| Target | Description | Time |
|:---|:---|:---|
| `all` | Full build + test + verify | ~2 min |
| `freeride` | Build proxy only | ~5 sec |
| `gastown` | Build GasTown submodule | ~10 sec |
| `test` | Run tests only | ~30 sec |
| `clean` | Clean artifacts | ~2 sec |

## Integration with GasTown

### Via Formula
```bash
# Create a build wisp
gt mol wisp create freeride-build --var target=all
```

### Via Assignment
```bash
# Assign to builder polecat
gt assign "Build and verify" testrig/builder --type=task
```

### Via NATS (for automated triggers)
```bash
# Publish build trigger
nats-pub gt.build.trigger.testrig '{"target":"all"}'
```

## Build Pipeline

1. **Build Freeride Proxy**
   - Compile Go binary
   - Output: `freeride` binary

2. **Test Freeride**
   - Run proxy tests (13 tests)
   - Verify model routing
   - Test tool extraction

3. **Build GasTown**
   - Compile submodule
   - All packages build cleanly

4. **Test GasTown Core**
   - Session management
   - Command handling
   - Provider interfaces

5. **Verify Proxy**
   - Check proxy is running
   - Count available models
   - Test endpoint connectivity

## Logs

Build output is written to:
- **Console**: Real-time output during build
- **File**: `testrig/build.log` (persistent)
- **NATS**: `gt.build.status` topic (for automation)

## Troubleshooting

| Issue | Solution |
|:---|:---|
| Build fails | Check `build.log` for details |
| Proxy not running | `./freeride --debug > freeride_live.log 2>&1 &` |
| Tests fail | Run `go test -v ./...` manually |
| Dolt issues | `gt dolt status` then `gt dolt start` |
| Out of space | `gt doctor --fix` or `go clean -cache` |

## Configuration

### Environment Variables
```bash
export OPENAI_BASE_URL="http://localhost:11434/v1"
export OPENAI_API_KEY="dummy"
```

### Proxy Settings
Edit `../models.yaml` to configure available models.

### GasTown Settings
Edit `settings/config.json` to configure build behavior.

## Automation

### Cron Job (every 15 minutes)
```bash
*/15 * * * * cd /home/stevef/dev/freeride/testrig && ./gt-build-trigger.sh
```

### Git Hook (pre-commit)
```bash
# In .git/hooks/pre-commit
exec /home/stevef/dev/freeride/testrig/build.sh test
```

### GasTown Patrol
Add to `mayor/daemon.json`:
```json
{
  "patrols": {
    "build_check": {
      "enabled": true,
      "interval": "30m",
      "script": "./testrig/gt-build-trigger.sh"
    }
  }
}
```

## Files

| File | Purpose |
|:---|:---|
| `build.sh` | Main build orchestration |
| `gt-build-manager.sh` | Build controller (start/status/logs) |
| `gt-build-trigger.sh` | Automated trigger for CI/CD |
| `settings/config.json` | Rig-specific build configuration |
| `build.log` | Persistent build output |
