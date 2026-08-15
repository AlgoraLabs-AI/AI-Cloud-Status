---
name: Bug report
about: Something misbehaves — a wrong reading, a crash, a UI problem
title: "[bug] "
labels: bug
---

<!--
The app can fill most of this in for you: Help → Report a bug opens this form
with the version and platform already filled, and names which files are worth
attaching. Everything below is only here for people who got here another way.
-->

### What happened



### What you expected instead



### Steps to reproduce

1.
2.
3.

### Environment

- Version: <!-- Help → About -->
- OS / arch:

### Logs

By default the app writes no diagnostic log — that is deliberate, not an
oversight. To capture one, turn on developer mode and reproduce:

```
setx ACS_DEV 1      # Windows, then relaunch (undo with: setx ACS_DEV "")
ACS_DEV=1 ./acs     # macOS / Linux
```

Then attach `acs.log` from the app's data folder:

| OS | Folder |
| --- | --- |
| Windows | `%AppData%\AI-Cloud-Status\` |
| macOS | `~/Library/Application Support/AI-Cloud-Status/` |
| Linux | `~/.config/AI-Cloud-Status/` |

If a provider is reading wrong, `alert-log.jsonl` and the matching file under
`feed-samples/` are what make it diagnosable without guessing.

> **Please skim what you attach.** A custom URL check you monitor shows up in
> these files, and its query string may contain a token.
