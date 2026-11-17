# Spec Delta

## MODIFIED Requirements

### Requirement: OnError hook
Consumers SHALL be able to register a hook that is called once per handled error with the request context, the original error and the resolved problem. The library SHALL NOT log by itself. A later `OnError` SHALL replace an earlier one. `AddErrorHook` SHALL add hooks that `OnError` never replaces. Each report SHALL call the `OnError` hook first, then the added hooks in the order added.

#### Scenario: Hook sees the original error
- **Given** a hook that records its calls
- **When** the handler writes `<error>`
- **Then** the hook was called `<calls>` times with error `<error>` and problem status `<status>`

##### Examples:
| error | calls | status |
|-------|-------|--------|
| `errors.New("pq: connection refused")` | 1 | 500 |
| `ErrVesselNotFound` (mapped to 404) | 1 | 404 |

#### Scenario: Replaced and added hooks
- **Given** a handler built with `<options>`, where each hook records its name when called
- **When** the handler writes `errors.New("boom")`
- **Then** the hooks were called in the order `<calls>`
- **And** each call received the error `boom` and problem status 500

##### Examples: registered
| options | calls |
|---------|-------|
| `OnError(A)` | A |
| `AddErrorHook(B)` | B |
| `OnError(A)`, `AddErrorHook(B)`, `AddErrorHook(C)` | A, B, C |
| `AddErrorHook(B)`, `OnError(A)` | A, B |
| `OnError(A)`, `OnError(A2)`, `AddErrorHook(B)` | A2, B |
| `AddErrorHook(B)`, `AddErrorHook(B)` | B, B |

##### Examples: misuse
| options | calls |
|---------|-------|
| `AddErrorHook(B)`, `OnError(nil)` | B |
| `OnError(A)`, `OnError(nil)` | (none) |
| `AddErrorHook(nil)` | (none) |
| `OnError(A)`, `AddErrorHook(nil)`, `AddErrorHook(C)` | A, C |

#### Scenario: Every hook receives notes and silent requests reach none
- **Given** a handler with `OnError(A)` and `AddErrorHook(B)`
- **When** `<event>`
- **Then** hook A received `<received>`
- **And** hook B received `<received>`

##### Examples:
| event | received |
|-------|----------|
| the handler writes `ErrX` after a registered mapper panics | a panic error, then `ErrX` |
| a mapper returns status 200 for `ErrX` | an error wrapping `ErrInvalidStatus`, then `ErrX` |
| type discovery answers `GET /problems/unknown` with 404 | (none) |
| type discovery answers `GET /problems/unknown` with 404 while an enricher panics | (none) |
| the handler writes a nil error | (none) |

#### Scenario: Same hooks in every adapter
- **Given** `<adapter>` configured with `OnError(A)` and `AddErrorHook(B)`
- **When** a handler returns `errors.New("boom")`
- **Then** the response status is 500
- **And** the hooks were called in the order `<calls>`, each with the error `boom`

##### Examples:
| adapter | calls |
|---------|-------|
| net/http | A, B |
| gin middleware | A, B |
| fiber error handler | A, B |

### Requirement: Faulty consumer code never breaks resolution
A mapper, describer or enricher that panics SHALL NOT crash the request. A panicking mapper or describer SHALL be treated as no match, a panicking enricher SHALL be skipped. Each panic SHALL be reported to the hooks. A panicking hook SHALL be ignored, and the hooks after it SHALL still be called.

#### Scenario: Consumer code panics
- **Given** `<faulty>` and a consumer mapper `ErrX` to 409 registered after it
- **When** the handler writes `ErrX`
- **Then** the status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| faulty | status | hook |
|--------|--------|------|
| a mapper that panics, registered first | 409 | a panic error, then `ErrX` |
| a describer error that panics wrapping `ErrX` | 409 | a panic error, then `ErrX` |
| an enricher that panics | 409 | a panic error, then `ErrX` |
| a hook that panics | 409 | (hook panics, response unaffected) |
| a nil mapper in the option list | 409 | `ErrX` |
| a nil enricher in the option list | 409 | `ErrX` |

#### Scenario: A panicking hook does not silence the others
- **Given** a handler with `<hooks>`, where `P` panics with `<value>` and the others record their calls
- **When** the handler writes `ErrX`
- **Then** the status is 409
- **And** the recording hooks were called in the order `<calls>`, each with `ErrX`

##### Examples:
| hooks | value | calls |
|-------|-------|-------|
| `OnError(P)`, `AddErrorHook(B)` | `"boom"` | B |
| `OnError(A)`, `AddErrorHook(P)`, `AddErrorHook(C)` | `"boom"` | A, C |
| `AddErrorHook(B)`, `AddErrorHook(P)` | `nil` | B |
| `OnError(A)`, `AddErrorHook(P)`, `AddErrorHook(C)` | `http.ErrAbortHandler` | A, C |
