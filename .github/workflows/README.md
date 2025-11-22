# GitHub Actions Workflows

This directory contains automated workflows for building and releasing the model-manager application.

## Workflows

### 1. `docker-build.yml` - Continuous Integration

**Trigger:** Push to `main` branch (when code changes)

**What it does:**
- Builds multi-architecture Docker images (linux/amd64, linux/arm64)
- Pushes to GitHub Container Registry (GHCR)
- Tags images as:
  - `latest` - Always points to the latest main branch build
  - `main-<sha>` - Specific commit SHA for traceability

**Use case:** Automatic builds on every code change to ensure the latest code is always available as a Docker image.

### 2. `release.yml` - Release Automation

**Trigger:** Push of a version tag (e.g., `v1.0.0`, `v2.1.3`)

**What it does:**
- Builds multi-architecture Docker images (linux/amd64, linux/arm64)
- Creates semantic version tags (e.g., `v1.0.0`, `v1.0`, `v1`, `latest`)
- Generates a changelog from git commits or CHANGELOG.md
- Creates a GitHub Release with:
  - Release notes
  - Docker image information
  - Installation instructions

**Use case:** Formal versioned releases with full documentation.

## How to Use

### For Development (Continuous Builds)

Simply push code changes to the `main` branch:

```bash
git add .
git commit -m "feat: add new feature"
git push origin main
```

The `docker-build.yml` workflow will automatically build and push the `latest` tag.

### For Releases

1. **Update version in your code** (if applicable)

2. **Create and push a version tag:**

```bash
# Create a new version tag
git tag -a v1.0.0 -m "Release version 1.0.0"

# Push the tag to trigger the release workflow
git push origin v1.0.0
```

3. **The workflow will:**
   - Build multi-arch images
   - Tag them with semantic versions (v1.0.0, v1.0, v1, latest)
   - Create a GitHub Release

4. **Optionally, create a CHANGELOG.md** before tagging:

```markdown
## v1.0.0 (2024-11-22)

### Features
- Added model activation API
- Implemented git-sync integration

### Bug Fixes
- Fixed ARM64 compatibility

### Breaking Changes
- None
```

## Docker Image Tags

After a release (`v1.2.3`), the following tags are available:

- `ghcr.io/oremus-labs/model-manager:v1.2.3` - Exact version
- `ghcr.io/oremus-labs/model-manager:v1.2` - Minor version
- `ghcr.io/oremus-labs/model-manager:v1` - Major version
- `ghcr.io/oremus-labs/model-manager:latest` - Latest release

All tags support both `linux/amd64` and `linux/arm64` platforms.

## Requirements

- Repository must have GitHub Actions enabled
- No additional secrets required (uses built-in `GITHUB_TOKEN`)
- GHCR package must be set to public or have appropriate access

## Caching

Both workflows use GitHub Actions cache to speed up builds:
- Docker layer caching
- Multi-stage build optimization

This significantly reduces build times for subsequent runs.
