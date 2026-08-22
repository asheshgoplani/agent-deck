#!/usr/bin/env bash
set -euo pipefail

# Fake-release fixture: no real release feed or installed binary is touched.
go test ./internal/update -run 'TestAutoInstallOrderingFixture|TestLaunchd|TestPerformVerifiedUpdateRefusesHomebrewManagedBinary' -count=1
