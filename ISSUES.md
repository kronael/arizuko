# Issues

## Critical (security/data)

### 1. Sensitive credentials in repo ✅ RESOLVED

**File**: `0xaida.py`
**Issue**: Twitter/X session cookies exposed in untracked file
**Impact**: Session hijacking risk if accidentally committed
**Resolution**: Deleted file, added `*.py` to .gitignore

## High (build artifacts)

### 2. Build artifacts not gitignored ✅ RESOLVED

**Files**: `proxyd/proxyd`, `proxyd/webd`
**Issue**: Compiled binaries present in working dir, not in .gitignore
**Impact**: Repo bloat, merge conflicts
**Resolution**: Added to .gitignore (proxyd/proxyd, proxyd/webd patterns)

## Medium (documentation)

### 3. Spec mismatch: dashd implementation status ✅ RESOLVED

**File**: `specs/7/25-dashboards.md`
**Issue**: Spec said "daemon shell only, no HTML templates yet" but dashd/main.go contains working inline HTML templates for all 5 dashboard pages
**Impact**: Misleading documentation
**Resolution**: Updated spec to "shipped (partial)" with accurate implementation summary

### 4. Distillation artifact untracked ✅ RESOLVED

**File**: `.distill/final.md`
**Issue**: Distillation doc not gitignored
**Impact**: Workspace clutter
**Resolution**: Deleted .distill/ directory, added to .gitignore

### 5. Extension sidecar status unclear ✅ RESOLVED

**File**: `specs/7/2-extensions.md`
**Issue**: Spec marked "planning" with "sidecar pending" note, but container/sidecar.go fully implemented
**Impact**: Misleading documentation
**Resolution**: Updated spec to "shipped (partial)" — sidecars working, plugins deferred

## Low (partial implementations)

### 6. Control chat multi-op notifications pending

**File**: `specs/7/20-control-chat.md`
**Issue**: Spec marked "shipped (partial)" - multi-operator support deferred
**Impact**: Single-operator limitation
**Status**: Design complete, intentionally deferred

### 7. Missing test coverage for daemons

**Files**: `gated/`, `onbod/`, `dashd/`, `proxyd/`, `webd/`, `discd/`
**Issue**: 6 daemon packages have no test files
**Impact**: Changes to daemons lack test safety net
**Priority**: Low (integration tests in tests/ cover most flows)
**Recommendation**: Add unit tests for daemon-specific logic when refactoring

### 8. Dashboard advanced features pending

**File**: `specs/7/25-dashboards.md`
**Issue**: Basic dashboards work but spec lists pending features (banner health, expandable sections, error details, onboarding section, flow viz, route editor)
**Impact**: Limited operator visibility into system state
**Priority**: Low (basic monitoring works)
**Status**: Tracked in spec, implementation deferred
