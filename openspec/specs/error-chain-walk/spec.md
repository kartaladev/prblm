# error-chain-walk Specification

## Purpose

Bounds how the library inspects an error chain. Every error is walked once, in a fixed order, within a fixed budget, so a cyclic, deep, exploding or hostile error is answered at once with the safe 500. It never hangs, crashes or leaks.

## Requirements

### Requirement: Each error is walked once in errors.Is order
The library SHALL inspect an error's chain through a single walk: the root first, then depth first, each `Unwrap() []error` in order, as `errors.Is` and `errors.As` visit it. Every resolution step SHALL use that walk, so first-match precedence stays what `errors.Is` order gives.

#### Scenario: First match follows errors.Is order
- **Given** a handler with zero configuration
- **When** the handler writes `<error>` for `GET /v1/vessels/7`
- **Then** the response status is `<status>`

##### Examples:
| error | status |
|-------|--------|
| `fmt.Errorf("vessel 7: %w", prblm.ErrNotFound)` | 404 |
| `errors.Join(prblm.ErrNotFound, prblm.ErrConflict)` | 404 |
| `errors.Join(errors.Join(errors.New("a"), prblm.ErrConflict), prblm.ErrNotFound)` | 409 |
| `errors.Join(prblm.New(422), prblm.ErrNotFound)` | 422 |
| `prblm.WithStatus(prblm.ErrNotFound, 410)` | 410 |

##### Examples: degenerate chains
| error | status |
|-------|--------|
| an error whose `Unwrap() error` returns nil | 500 |
| an error whose `Unwrap() []error` returns an empty slice | 500 |
| an error whose `Unwrap() []error` returns `[nil, prblm.ErrNotFound]` | 404 |

### Requirement: The walk examines at most 1,000 entries
Every entry the walk examines SHALL cost 1. This covers the root, each node, each repeated visit of a node, each nil entry of an `Unwrap() []error`, and each typed nil. Nothing is deduplicated. An entry SHALL be counted before any of its methods is called. The walk SHALL abort when the entries counted plus those waiting to be examined would exceed 1,000.

#### Scenario: Budget boundary
- **Given** a handler with zero configuration and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** the hook received `<hook>`

##### Examples: within the budget
| error | status | hook |
|-------|--------|------|
| `prblm.ErrNotFound` wrapped 999 times with `%w` (1,000 entries) | 404 | the error |
| `prblm.WithStatus(prblm.ErrNotFound wrapped 998 times with %w, 410)` (1,000 entries) | 410 | the error |
| an error whose `Unwrap() []error` returns 998 nil entries and `prblm.ErrNotFound` (1,000 entries) | 404 | the error |

##### Examples: past the budget
| error | status | hook |
|-------|--------|------|
| `prblm.ErrNotFound` wrapped 1,000 times with `%w` (1,001 entries) | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |
| an error whose `Unwrap() []error` returns 1,000 entries | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |
| an error whose `Unwrap() []error` returns 20,000 copies of itself | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |
| 1,000 entries, then an entry whose `Unwrap` panics with `"boom"` | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |
| `prblm.WithStatus(prblm.ErrNotFound wrapped 999 times with %w, 410)` (1,001 entries) | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |

### Requirement: Only the application's status hints are entries
A status hint the application attaches with `prblm.WithStatus` SHALL be an entry of the chain like any other. The status hint an adapter takes from the framework (the code of gin's `c.AbortWithError`, the code of a `*fiber.Error`) SHALL NOT be an entry and SHALL cost nothing, so an error the walk could finish alone is still walked when an adapter answers it.

#### Scenario: Adapter status hints cost nothing
- **Given** `<adapter>` with a recording hook
- **When** a handler at `GET /v1/vessels/7` `<answers>`
- **Then** within 1 second the response status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| adapter | answers | status | hook |
|---------|---------|--------|------|
| gin `Middleware` with the mapper `prblm.Status(ErrLocked, 423)` | calls `c.AbortWithError(409, err)`, `err` being `ErrLocked` wrapped 999 times with `%w` (1,000 entries) | 423 | the error with the hint |
| gin `Middleware` with the mapper `prblm.Status(ErrLocked, 423)` | calls `c.AbortWithError(409, err)`, `err` being `ErrLocked` wrapped 1,000 times with `%w` (1,001 entries) | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error with the hint |
| fiber `ErrorHandler` | returns `fiber.NewError(409, "conflict")` wrapped 999 times with `%w` (1,000 entries) | 409 | the error with the hint |
| fiber `ErrorHandler` | returns `fiber.NewError(409, "conflict")` wrapped 1,000 times with `%w` (1,001 entries) | 500 | an `ErrUnwalkableError` note with reason `budget`, then the error |

### Requirement: Cyclic and exploding chains are answered at once
An error whose chain loops back on itself, or fans out past the budget, SHALL be answered with the safe 500 without hanging and without overflowing the stack. The hooks SHALL receive an `ErrUnwalkableError` note with reason `budget`, then the error.

#### Scenario: Cycles and fan-out
- **Given** a handler built with `<adapter>` with zero configuration and a recording hook
- **When** a handler returns `<error>` for `GET /v1/vessels/7`
- **Then** within 1 second the client receives status 500 with `Content-Type: application/problem+json` and the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received an `ErrUnwalkableError` note with reason `budget`, then the error

##### Examples: net/http
| adapter | error |
|---------|-------|
| net/http `Handler.Wrap` | an error whose `Unwrap() error` returns itself |
| net/http `Handler.Wrap` | two errors whose `Unwrap() error` return each other |
| net/http `Handler.Wrap` | an error whose `Unwrap() []error` contains itself |
| net/http `Handler.Wrap` | an `errors.Join` whose entries contain the join |
| net/http `Handler.Wrap` | 25 nested `errors.Join(n, n)` |

##### Examples: other adapters
| adapter | error |
|---------|-------|
| gin `Middleware` | an error whose `Unwrap() error` returns itself |
| gin `Middleware` | an `errors.Join` whose entries contain the join |
| fiber `ErrorHandler` | an error whose `Unwrap() error` returns itself |
| fiber `ErrorHandler` | an `errors.Join` whose entries contain the join |

### Requirement: Typed nils are skipped
A nil pointer in the chain SHALL count against the budget, SHALL NOT be matched, and none of its methods SHALL be called. A typed-nil root SHALL give an empty walk, not an abort. Nil maps, slices, funcs and channels SHALL NOT be treated as nil.

#### Scenario: Typed nil in the chain
- **Given** a handler with zero configuration and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| error | status | hook |
|-------|--------|------|
| `errors.Join((*prblm.Problem)(nil), prblm.New(404))` | 404 | the error |
| `errors.Join((*http.MaxBytesError)(nil), sql.ErrConnDone)` | 500 | the error |
| `errors.Join((*json.SyntaxError)(nil), prblm.ErrConflict)` | 409 | the error |
| a nil pointer to an error type whose `Is` method has a value receiver | 500 | the error |
| a nil pointer to an error type whose `Unwrap` method has a value receiver, joined with `prblm.ErrNotFound` | 404 | the error |
| a typed-nil `*MyError` (no methods but `Error`) stored in an `error` | 500 | the error |
| a nil slice of a slice type implementing `error` and `Is(prblm.ErrNotFound)` | 404 | the error |
| a nil pointer of the adapters' status-hint wrapper type | 500 | the error |
| the adapters' status-hint wrapper with status 409 around a nil pointer of that same type | 409 | the error |

### Requirement: A nil adapter hint wrapper is a typed nil
A nil pointer of the type the adapters wrap their status hint in SHALL be an ordinary typed nil: it SHALL carry no hint, and neither the core, nor a helper mapper called directly, nor an adapter SHALL panic on it. A consumer can make one with `reflect.Zero` from the error its hook receives.

#### Scenario: Typed-nil adapter hint wrapper
- **Given** `<setup>` and a recording hook
- **When** `<call>` with a nil pointer of the adapters' status-hint wrapper type
- **Then** `<outcome>`

##### Examples:
| setup | call | outcome |
|-------|------|---------|
| the helper `prblm.Status(prblm.ErrNotFound, 410)` | `MapError` is called directly | it returns nil and does not panic |
| a gin engine with `Middleware` | a handler records the error in `c.Errors` | the response is a 500 `application/problem+json` problem, and the hook is called once |
| a fiber app with `ErrorHandler` | a handler returns the error | the response is the safe 500 problem, and the hook receives the error once |

### Requirement: An As that sets nothing does not match
When an error's `As` method reports a match for a pointer or interface target and leaves the target nil, the step SHALL treat that entry as no match. For any other target type, a reported match SHALL count as a match, as `errors.As` does.

#### Scenario: Empty As results
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| setup | error | status | hook |
|-------|-------|--------|------|
| zero configuration | an error whose `As` method returns true and sets nothing | 500 | the error |
| built-ins disabled | an error whose `As` method returns true and sets nothing | 500 | the error |
| zero configuration | `errors.Join(an error whose As method returns true and sets nothing, prblm.New(404))` | 404 | the error |
| consumer mapper `prblm.As(func(*MyError) *prblm.Problem { return prblm.New(409) })` | an error whose `As` method returns true and leaves a `*MyError` target nil | 500 | the error |
| consumer mapper `prblm.As(func(Code) *prblm.Problem { return prblm.New(409) })` with `Code` an `int` error type | an error whose `As` method returns true and sets a `Code` target to 0 | 409 | the error |

### Requirement: A panic in an error's own method aborts the walk
A panic in an error's `Unwrap`, `Is` or `As` method, or in comparing an entry with a target, SHALL abort the walk, whatever the panic value, `nil` included. The answer SHALL be the safe 500 even when another step would have matched. The hooks SHALL receive an `ErrUnwalkableError` note with reason `panic` that wraps a panic error holding the value and the panicking frames, then the error.

#### Scenario: Error methods panic
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>` for `GET /v1/vessels/7`
- **Then** the response status is 500 and the body is `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the body does not contain `boom`
- **And** the hook received an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `<value>`, then the error

##### Examples:
| setup | error | value |
|-------|-------|-------|
| zero configuration | an error whose `Unwrap() error` panics with `"boom"` | `"boom"` |
| zero configuration | an error whose `Unwrap() []error` panics with `"boom"` | `"boom"` |
| zero configuration | an error whose `As` method panics with `"boom"` | `"boom"` |
| zero configuration | an error whose `Is` method panics with `"boom"` | `"boom"` |
| zero configuration | an error whose `Unwrap() error` calls `panic(nil)` | a `*runtime.PanicNilError` |
| built-ins disabled | an error whose `As` method panics with `"boom"` | `"boom"` |
| consumer mapper `prblm.Status(ErrX, 409)` | `errors.Join(ErrX, an error whose As method panics with "boom")` | `"boom"` |
| zero configuration | `prblm.WithStatus(an error whose Is method panics with "boom", 404)` | `"boom"` |
| consumer mapper `prblm.Status(T{v: []int{1}}, 409)`, `T` a comparable struct type with an `any` field | `T{v: []int{1}}` | a runtime error about comparing an uncomparable type |

#### Scenario: panic(nil) under GODEBUG=panicnil=1
- **Given** a test binary run with `GODEBUG=panicnil=1` and a handler with a recording hook
- **When** the handler writes an error whose `Unwrap() error` calls `panic(nil)`
- **Then** the response status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| status | hook |
|--------|------|
| 500 | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `nil`, then the error |

#### Scenario: Error methods panic without a hook
- **Given** a handler with zero configuration and no hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** nothing panics out of the handler

##### Examples:
| error | status |
|-------|--------|
| an error whose `As` method panics with `"boom"` | 500 |
| an error whose `Unwrap() error` returns itself | 500 |

### Requirement: An aborted walk is reported once
For each error whose walk aborts, the hooks SHALL receive exactly one note, with the 500 problem being sent, before the usual report of the error. The note SHALL match `prblm.ErrUnwalkableError` with `errors.Is` and SHALL name its reason. Its message SHALL NOT include the error's text; for reason `panic`, it SHALL name only the panic value's type, as `%T` prints it, and SHALL call no method of the value. A silenced request SHALL report nothing.

#### Scenario: One note per error
- **Given** a handler with hooks `<hooks>` recording their calls
- **When** the handler writes an error whose `Unwrap() error` returns itself
- **Then** the hooks received `<calls>`

##### Examples:
| hooks | calls |
|-------|-------|
| `OnError(A)` | A: note (problem status 500), A: the error (problem status 500) |
| `OnError(A)`, `AddErrorHook(B)` | A: note, B: note, A: the error, B: the error |
| `OnError(P)` where `P` panics, `AddErrorHook(B)` | B: note, B: the error |
| (none) | (nothing; the response is still the safe 500) |

#### Scenario: Note text
- **Given** a handler with a recording hook
- **When** the handler writes `<error>`
- **Then** the note's message is `<message>`, never the text of the error or of the panic value, even when the error panics with itself
- **And** for reason `panic`, the note wraps the panic error, so a hook that chooses to inspect the value reaches it with `errors.As`

##### Examples:
| error | message |
|-------|---------|
| an error with text `secret-dsn` whose `Unwrap() error` returns itself | `prblm: error chain cannot be walked: budget` |
| an error with text `secret-dsn` whose `As` method panics with `"boom"` | `prblm: error chain cannot be walked: panic: string` |
| an error with text `secret-dsn` whose `Unwrap() error` panics with the error itself, of type `selfPanicUnwrapError` | `prblm: error chain cannot be walked: panic: prblm_test.selfPanicUnwrapError` |
| an error whose `Is` method panics with a value of type `*selfPanicError`, whose `Error` method panics with itself | `prblm: error chain cannot be walked: panic: *prblm_test.selfPanicError` |
| an error whose `Unwrap() error` calls `panic(nil)` | `prblm: error chain cannot be walked: panic: *runtime.PanicNilError` |
| an error whose `Unwrap() error` calls `panic(nil)`, in a binary run with `GODEBUG=panicnil=1` | `prblm: error chain cannot be walked: panic: <nil>` |

### Requirement: Consumer code sees the error as returned
Mappers written by the consumer, describers' `Problem` methods, enrichers and hooks SHALL receive the error exactly as returned, including any typed nils, and SHALL keep the documented rule that their own panics count as no match or are skipped. Mappers built with `prblm.Is`, `prblm.As` and `prblm.Status` SHALL match by the bounded walk.

#### Scenario: What consumer code receives
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** `<observed>`

##### Examples:
| setup | error | status | observed |
|-------|-------|--------|----------|
| a consumer `MapperFunc` that records its argument and returns nil | `errors.Join((*MyError)(nil), prblm.ErrNotFound)` | 404 | the mapper received the joined error unchanged |
| consumer mapper `prblm.As(func(*MyError) *prblm.Problem { return prblm.New(409) })` | `errors.Join((*MyError)(nil), prblm.ErrNotFound)` | 404 | the mapper's function was not called |
| a consumer `MapperFunc` that panics with `"boom"` | `prblm.ErrNotFound` | 404 | the hook received a panic error with value `"boom"`, then the error |
| a consumer `MapperFunc` that calls `panic(nil)` | `prblm.ErrNotFound` | 404 | the hook received a panic error, then the error |

### Requirement: The walk is safe for concurrent use
Walking SHALL keep no shared state, so concurrent requests with cyclic, deep or panicking errors get independent responses.

#### Scenario: Concurrent hostile errors
- **Given** one handler with zero configuration
- **When** `<n>` goroutines write `<error>` at the same time under the race detector
- **Then** every response has status `<status>` and no data race is reported

##### Examples:
| n | error | status |
|---|-------|--------|
| 64 | an error whose `Unwrap() error` returns itself | 500 |
| 64 | an error whose `As` method panics with `"boom"` | 500 |
| 64 | `fmt.Errorf("x: %w", prblm.ErrNotFound)` | 404 |
