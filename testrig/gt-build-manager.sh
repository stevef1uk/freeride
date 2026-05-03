#!/bin/bash
# GasTown Build Manager
# Usage: ./gt-build-manager.sh [command]
# Commands: status, start, logs, clean

set -e

TOWN_ROOT="/home/stevef/dev/freeride"
RIG="testrig"
BUILD_SCRIPT="$TOWN_ROOT/testrig/build.sh"
BUILD_LOG="$TOWN_ROOT/testrig/build.log"
PID_FILE="$TOWN_ROOT/testrig/.build.pid"

show_status() {
    echo "=== GasTown Build Status ==="
    echo ""
    
    if [ -f "$PID_FILE" ] && kill -0 $(cat "$PID_FILE") 2>/dev/null; then
        echo "Build Status: 🟢 Running (PID: $(cat $PID_FILE))"
    else
        echo "Build Status: ⚪ Idle"
    fi
    
    echo "Last Build: $(tail -1 "$BUILD_LOG" 2>/dev/null || echo 'No builds yet')"
    echo "Build Script: $BUILD_SCRIPT"
    echo "Build Log: $BUILD_LOG"
    echo ""
    
    # Show proxy status
    if curl -s http://localhost:11434/v1/models > /dev/null 2>&1; then
        MODELS=$(curl -s http://localhost:11434/v1/models | grep -c '"id"' || echo "0")
        echo "Freeride Proxy: 🟢 Running ($MODELS models)"
    else
        echo "Freeride Proxy: 🔴 Not running"
    fi
    
    # Show git status
    cd "$TOWN_ROOT"
    echo "Git Branch: $(git branch --show-current 2>/dev/null || echo 'unknown')"
    echo "Uncommitted Changes: $(git status --short | wc -l) files"
}

start_build() {
    echo "Starting GasTown build..."
    
    if [ -f "$PID_FILE" ] && kill -0 $(cat "$PID_FILE") 2>/dev/null; then
        echo "Build already running (PID: $(cat $PID_FILE))"
        exit 1
    fi
    
    nohup "$BUILD_SCRIPT" all >> "$BUILD_LOG" 2>&1 &
    echo $! > "$PID_FILE"
    echo "Build started (PID: $!)"
    echo "Tail logs: tail -f $BUILD_LOG"
}

show_logs() {
    if [ -f "$BUILD_LOG" ]; then
        echo "=== Last 50 lines of build log ==="
        tail -50 "$BUILD_LOG"
    else
        echo "No build log found"
    fi
}

clean_build() {
    echo "Cleaning build artifacts..."
    rm -f "$PID_FILE"
    "$BUILD_SCRIPT" clean
    echo "✓ Cleaned"
}

case "${1:-status}" in
    status)
        show_status
        ;;
    start)
        start_build
        ;;
    logs)
        show_logs
        ;;
    clean)
        clean_build
        ;;
    *)
        echo "Usage: $0 [status|start|logs|clean]"
        exit 1
        ;;
esac
