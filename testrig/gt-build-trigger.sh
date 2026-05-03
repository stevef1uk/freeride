#!/bin/bash
# GasTown Automated Build Trigger
# This script can be called by GasTown polecats to execute builds

set -e

TOWN_ROOT="/home/stevef/dev/freeride"
RIG="testrig"
BUILD_LOG="/home/stevef/dev/freeride/testrig/build.log"

echo "$(date '+%Y-%m-%d %H:%M:%S') - GasTown Build Triggered" >> "$BUILD_LOG"

# Change to town root
cd "$TOWN_ROOT"

# Run the build
echo "Starting automated build..." >> "$BUILD_LOG"
if ./testrig/build.sh all >> "$BUILD_LOG" 2>&1; then
    echo "$(date '+%Y-%m-%d %H:%M:%S') - ✓ Build successful" >> "$BUILD_LOG"
    
    # Send success notification via NATS if available
    if command -v nats-pub >/dev/null 2>&1; then
        nats-pub gt.build.status "$RIG: build successful" 2>/dev/null || true
    fi
    
    exit 0
else
    echo "$(date '+%Y-%m-%d %H:%M:%S') - ✗ Build failed" >> "$BUILD_LOG"
    
    # Send failure notification
    if command -v nats-pub >/dev/null 2>&1; then
        nats-pub gt.build.status "$RIG: build failed" 2>/dev/null || true
    fi
    
    exit 1
fi
