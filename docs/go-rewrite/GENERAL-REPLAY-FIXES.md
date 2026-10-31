# General paired replay: error-class corrections

## Observed result

The first general replay executed 20 workflows and 94 steps: 82 steps passed,
12 failed, and none were blocked. The failed evidence is captured in
`general-workflow-1-failures.json` (SHA-256
`a008f37e0d8f850b276c2318aa023601299589f91589c7a07650bd848a830da2`).
All failed workflows had matching final fixture state; cleanup reported no errors.
This patch does not turn that first run into a pass. A fresh paired replay remains
required after the candidate is rebuilt.

## Eleven implementation errors

Eleven steps differed only in the exception class rendered in both
`content[0].text` and `structuredContent.error`: TypeScript emitted
`CodexProError: ...`; Go emitted `Error: ...`. The source explicitly throws
`CodexProError` in these branches. The corresponding Go branches now return
`wire.Errorf("", ...)`, preserving the message, lack of error code, and complete
existing envelope. The shared error renderer is unchanged.

| Cases | Source evidence | Corrected package |
| --- | --- | --- |
| Read beyond EOF | `src/fsOps.ts:529-531` | files/core.go |
| Stale expected SHA-256 | `src/fsOps.ts:348-350` | files/core.go |
| Overlapping tagged edits | `src/fsOps.ts:733-735` | files/core.go |
| Tagged edit with no change | `src/fsOps.ts:786-788` | files/core.go |
| Unsupported image bytes | `src/imageOps.ts:63` | files/images.go |
| Missing attachment fields and disallowed scheme | `src/importOps.ts:81-86` | files/import.go |
| Insecure protocol and unapproved origin | `src/importOps.ts:130-144` | files/import.go |
| Configuration search with regex enabled | `src/searchOps.ts:1053-1054` | search/service.go |
| Ripgrep parse failure | `src/searchOps.ts:359-362` | search/primary.go |

These are targeted replacements. Native filesystem/process errors and the
ordinary `Error` exceptions thrown by skill lookup retain their existing class.
The regression tests check typed error identity, both rendered text locations,
the absence of new structured fields, and unchanged files after failed writes
and edits. Rejected import cases are arranged to fail before DNS or network work.

## One corpus expectation error

`FILE-SKILLS-10/exact-shadow` already produced equal decoded responses in the two
implementations. Raw records 219 and 221, and their normalized records 220 and
222, show an ordinary error for the same absolute-path skill request.

The source inventory displays workspace skill paths as literal
`$WORKSPACE/<relative path>` (`src/capabilitiesOps.ts:169-171`). Its loader compares
that displayed path literally with the supplied `path` (`:363`); it does not
resolve an absolute path to that display form. The fixture runner intentionally
substitutes an absolute side-specific workspace path for this argument. The
source therefore rejects it with an ordinary `Error: Skill not found...`
(`:365-372`), identically to Go.

The corpus now declares that unchanged call as `expect_error=True`. The exact
arguments, workflow and step identifiers, fixtures, order, assertions, comparison
logic, and 20-workflow/94-step count are unchanged. The Go regression additionally
checks that the same shadowed skill can be loaded by supplying the literal
displayed path, and that the absolute-path rejection remains an ordinary error.

## Validation

An offline corpus comparison confirms that the sole data change is
`expect_error` for this one step, and that captured normalized TS and Go results
for it are identical. Go execution and the fresh paired runtime replay are
performed on the isolated ssd candidates under the parent agent's bounded test
lane; this local staging environment has no Go toolchain.
