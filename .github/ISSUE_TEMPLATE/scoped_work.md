---
name: Scoped work
about: A piece of work one person can finish in a single cycle, with acceptance criteria
title: "feat(scope): "
labels: enhancement
---

## Summary

One or two sentences: what changes.

## Why it matters

The problem a user or maintainer has today. Say who it affects.

## Where this lives

Is it only in this repository, or does it also need a change in `eventparity-studio`?

Depends on: none | link the issue in this repository or in `eventparity-studio` that must land first.

If it spans both repositories, open a matching issue in the other one and link both ways. Do not ship the app side before the core side is released.

## Scoped options (only if there is a design decision)

- Option A: ...
- Option B: ...

State which one you recommend and why.

## Acceptance Criteria

- [ ] ...
- [ ] Tests are added or updated, and CI is green.
- [ ] Docs, `SPEC.md` or the changelog are updated where behavior changes.

## Tech Stack

Go 1.25

## Complexity

Pick one and be honest: trivial (a small, clearly bounded change), medium (standard work touching more than one area), high (an integration or an architecture change).
