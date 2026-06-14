# Maintainer Guide

This document outlines recurring operational tasks and procedures for maintaining the `runner-operator` project.

## Release Process

Releases are automated using the GitHub CLI (`gh`). A release consists of updating the Helm chart version and creating a GitHub release, which triggers the chart publication workflow.

### Steps to Release a New Version

1.  **Update Helm Chart Version**
    Modify `dist/chart/Chart.yaml` to set the new `version` and `appVersion`.
    ```yaml
    version: 0.3.2
    appVersion: "0.3.2"
    ```

2.  **Commit the Changes**
    Commit the version update to the `main` branch.
    ```bash
    git add dist/chart/Chart.yaml
    git commit -m "chore(release): prepare for v0.3.2"
    git push origin main
    ```

3.  **Create the GitHub Release**
    Use the `gh` CLI to create the release. This will automatically create the git tag and trigger the `release-chart.yaml` workflow.
    ```bash
    gh release create v0.3.2 --generate-notes
    ```

4.  **Verify the Release**
    - Check the [Releases page](https://github.com/mohamedhabas11/runner_operator/releases) on GitHub.
    - Monitor the [GitHub Actions](https://github.com/mohamedhabas11/runner_operator/actions) for the `Release Chart` workflow to ensure the Helm repository (gh-pages branch) is updated.

## Local Development & Testing

Refer to the [Development Guide](README.md#development) in the main README for information on local setup, running tests, and building the operator.

## Architecture & Design Decisions

Design rationale and architectural blueprints are maintained in the `arch/` and `ledger/` directories.
