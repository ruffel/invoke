# Unified Deploy Example

This example demonstrates how to use the `invoke` library to create a unified deployment tool.

## Overview

The goal is to show how the same deployment logic (`deploy.go`) can target different environments without changing the core code. It supports:

- **Local**: Deploys to the local filesystem.
- **SSH**: Deploys to a remote server.
- **Docker**: Deploys to a container.

## Code Structure

- **`main.go`**: Entry point. Sets up the `cobra` commands and flags.
- **`deploy.go`**: Core deployment logic using the `invoke.Environment` interface.
- **`provision.go`**: Helpers for provisioning ephemeral Docker/SSH environments.
- **`styles.go`**: UI styling definitions using Lipgloss.

### Quick Start

```bash
# Run locally
go run . local

# Run against ephemeral Docker container
go run . docker --ephemeral
```
