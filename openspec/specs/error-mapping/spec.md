# error-mapping Specification

## Purpose

Defines how a Go error becomes a problem: the consumer-injectable mapper contract, the resolution order, the status hint, request-scoped mappers, the safe fallback, and the hooks around mapping, including faulty consumer code.

## Requirements

### Requirement: Mapper contract
A mapper SHALL receive the request context and the error and return either a problem (a match) or nil (no match). No status code or other sentinel value SHALL signal "no match".

#### Scenario: Match or no match
- **Given** a consumer mapper that maps `ErrVesselNotFound` to status 404
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`

##### Examples:
| error | status |
|-------|--------|
| `ErrVesselNotFound` | 404 |
| `errors.New("other")` | 500 |

### Requirement: Sentinel and typed mapping helpers
The library SHALL provide helpers that build a mapper from a sentinel error (matched as `errors.Is` does), from an error type (matched as `errors.As` does, generic over the type), and from a sentinel plus a status alone. The helpers SHALL match over the bounded walk of the error chain: typed nils never match, and an `As` method that leaves a pointer or interface target nil does not match.

#### Scenario: Sentinel helper
- **Given** a mapper built for sentinel `ErrVesselNotFound` returning status 404
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`

##### Examples:
| error | status |
|-------|--------|
| `ErrVesselNotFound` | 404 |
| `fmt.Errorf("load: %w", ErrVesselNotFound)` | 404 |
| `errors.Join(io.EOF, ErrVesselNotFound)` | 404 |
| `errors.New("vessel not found")` | 500 |

#### Scenario: Typed helper exposes the value
- **Given** a mapper built for type `*RefusedError` that returns status 409 with extension `reasons` from the value
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>` and extension `reasons` is `<reasons>`

##### Examples:
| error | status | reasons |
|-------|--------|---------|
| `&RefusedError{Reasons: ["closed"]}` | 409 | `["closed"]` |
| `fmt.Errorf("x: %w", &RefusedError{Reasons: ["full"]})` | 409 | `["full"]` |
| `errors.New("refused")` | 500 | (none) |

##### Examples: typed nils and empty results
| error | status | reasons |
|-------|--------|---------|
| `errors.Join((*RefusedError)(nil), &RefusedError{Reasons: ["full"]})` | 409 | `["full"]` |
| `errors.Join((*RefusedError)(nil), prblm.ErrNotFound)` | 404 | (none) |
| an error whose `As` method returns true and leaves a `*RefusedError` target nil | 500 | (none) |

#### Scenario: Helper called directly
- **Given** a mapper built with `<helper>`
- **When** the application calls its `MapError` with `<error>` outside any handler
- **Then** the result is `<result>`
- **And** the call returns within 1 second

##### Examples:
| helper | error | result |
|--------|-------|--------|
| `prblm.Status(ErrVesselNotFound, 404)` | `fmt.Errorf("load: %w", ErrVesselNotFound)` | a problem with status 404 |
| `prblm.Status(ErrVesselNotFound, 404)` | an error whose `Unwrap() error` returns itself | nil |
| `prblm.As(func(*RefusedError) *prblm.Problem { return prblm.New(409) })` | an error whose `As` method panics with `"boom"` | nil |
| `prblm.As(func(*RefusedError) *prblm.Problem { return prblm.New(409) })` | `(*RefusedError)(nil)` | nil |
| `prblm.Status(ErrVesselNotFound, 404)` | `errors.Join(ErrVesselNotFound, an error whose As method panics with "boom")` | a problem with status 404 |

### Requirement: Resolution order and first match wins
The library SHALL resolve an error in this order and stop at the first match: (1) self-describing errors, (2) request-scoped mappers, (3) consumer mappers, (4) built-in mappers, (5) the status hint, (6) the fallback. Within one step, mappers SHALL run in registration order.

#### Scenario: Precedence between steps
- **Given** `<setup>`
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`

##### Examples:
| setup | error | status |
|-------|-------|--------|
| consumer mapper `*json.SyntaxError` to 400 (built-in maps it to 400 too) and one to 418 registered first | a `*json.SyntaxError` | 418 |
| consumer mapper `*json.UnmarshalTypeError` to 400 | a `*json.UnmarshalTypeError` | 400 |
| scoped mapper `ErrLocked` to 423 and consumer mapper `ErrLocked` to 409 | `ErrLocked` | 423 |
| consumer mapper `ErrLocked` to 409 | `WithStatus(ErrLocked, 400)` | 409 |
| no mappers | `WithStatus(ErrLocked, 400)` | 400 |
| consumer mapper `ErrLocked` to 409 | a problem with status 422 wrapping `ErrLocked` | 422 |

#### Scenario: First registered consumer mapper wins
- **Given** consumer mappers A (`ErrX` to 409) and B (`ErrX` to 410) registered as `<order>`
- **When** the handler resolves `ErrX`
- **Then** the resolved status is `<status>`

##### Examples:
| order | status |
|-------|--------|
| A, B | 409 |
| B, A | 410 |

### Requirement: Self-describing errors
An error chain that contains a problem, or an error that implements the self-describing contract, SHALL resolve to that problem without calling any mapper. The outermost self-describing error in the chain SHALL win; a typed nil in the chain is never self-describing. A received problem SHALL NOT count as self-describing; a problem the application takes out of it and returns SHALL.

#### Scenario: Problem or describer in the chain
- **Given** a consumer mapper that counts its calls
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`
- **And** the mapper was called `<calls>` times

##### Examples:
| error | status | calls |
|-------|--------|-------|
| a problem with status 409 | 409 | 0 |
| `fmt.Errorf("op: %w", problem with status 409)` | 409 | 0 |
| a domain error whose self-description has status 422 | 422 | 0 |
| a problem 400 wrapping a describer with status 422 | 400 | 0 |
| a describer returning nil | 500 | 1 |
| a received problem holding status 404 | 502 | 1 |
| the problem held by a received problem with status 404 (`&r.Problem`) | 404 | 0 |

##### Examples: typed nils
| error | status | calls |
|-------|--------|-------|
| `(*prblm.Problem)(nil)` stored in an `error` | 500 | 1 |
| `errors.Join((*prblm.Problem)(nil), problem with status 404)` | 404 | 0 |
| `errors.Join((*DomainError)(nil), problem with status 409)`, `*DomainError` a describer | 409 | 0 |
| an error whose `As` method returns true and leaves a `prblm.Describer` target nil, wrapping a problem with status 404 | 404 | 0 |

### Requirement: Resolved problems are copies
The library SHALL copy a resolved problem before finalizing or enriching it, so problems shared by many requests (for example, package-level values) are never modified.

#### Scenario: Shared problem reused concurrently
- **Given** a package-level problem with status 409 returned by a mapper, and an enricher that sets extension `trace_id` from the request
- **When** `<requests>` concurrent requests resolve it
- **Then** each response carries its own `trace_id`
- **And** the package-level problem still has no extensions and no instance

##### Examples:
| requests |
|----------|
| 1 |
| 100 |

### Requirement: Status hint
Callers SHALL be able to attach a status hint to an error. When no self-describing error or mapper matches, the hint SHALL produce an `about:blank` problem with that status and no detail. A hint outside 400–599 SHALL be ignored.

#### Scenario: Hint used when unmapped
- **Given** no consumer mappers
- **When** the handler resolves `WithStatus(errors.New("secret text"), <hint>)`
- **Then** the resolved status is `<status>`, the title is `<title>` and the response contains no `secret text`

##### Examples: valid hints
| hint | status | title |
|------|--------|-------|
| 404 | 404 | `Not Found` |
| 403 | 403 | `Forbidden` |
| 599 | 599 | (empty) |

##### Examples: ignored hints
| hint | status | title |
|------|--------|-------|
| 200 | 500 | `Internal Server Error` |
| 399 | 500 | `Internal Server Error` |
| 600 | 500 | `Internal Server Error` |
| 0 | 500 | `Internal Server Error` |
| -1 | 500 | `Internal Server Error` |

#### Scenario: Nested and wrapped hints
- **Given** no consumer mappers
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`

##### Examples:
| error | status |
|-------|--------|
| `WithStatus(WithStatus(err, 400), 404)` | 404 |
| `fmt.Errorf("x: %w", WithStatus(err, 401))` | 401 |
| `WithStatus(nil, 404)` | no error (nil) |

### Requirement: Request-scoped mappers
Consumers SHALL be able to attach mappers to a request context, for example in a route-group middleware. They apply only to that request and run before consumer mappers. Attaching to a context that already has scoped mappers SHALL add to them, the newer ones first.

#### Scenario: Scoped only on its route
- **Given** a scoped mapper `ErrLocked` to 423 attached for routes under `/v1/contracts`
- **When** `ErrLocked` is returned for `<path>`
- **Then** the response status is `<status>`

##### Examples:
| path | status |
|------|--------|
| `/v1/contracts/1` | 423 |
| `/v1/vessels/1` | 500 |

#### Scenario: Nested scopes
- **Given** an outer scope mapping `ErrLocked` to 423 and an inner scope mapping `ErrLocked` to 409
- **When** `ErrLocked` is returned from a route inside both scopes
- **Then** the response status is `<status>`

##### Examples:
| status |
|--------|
| 409 |

### Requirement: Built-in mappers
With zero configuration, the library SHALL map these standard-library errors with a generic detail and never the error text. Consumers SHALL be able to disable all built-ins. A bare `io.EOF` SHALL NOT be mapped: it can come from any reader, including upstream services, so it falls to the safe fallback.

#### Scenario: Built-in mapping
- **Given** a handler with `<builtins>`
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>` and the detail is `<detail>`

##### Examples: enabled (default)
| builtins | error | status | detail |
|----------|-------|--------|--------|
| default options | a `*json.SyntaxError` | 400 | `The request body is not valid JSON.` |
| default options | `io.ErrUnexpectedEOF` | 400 | `The request body is incomplete.` |
| default options | `io.EOF` | 500 | (empty) |
| default options | a `*json.UnmarshalTypeError` for field `age` | 422 | `The request body has a value of the wrong type.` |
| default options | a `*http.MaxBytesError` | 413 | `The request body is too large.` |
| default options | `context.DeadlineExceeded` | 504 | (empty) |

##### Examples: disabled
| builtins | error | status | detail |
|----------|-------|--------|--------|
| built-ins disabled | a `*json.SyntaxError` | 500 | (empty) |
| built-ins disabled | `context.DeadlineExceeded` | 500 | (empty) |

### Requirement: Safe fallback
An error that matches no step SHALL produce status 500, type `about:blank`, title `Internal Server Error`, no detail and no extensions except those added by enrichers. The error text SHALL never appear in the response.

#### Scenario: Unmapped error text never leaks
- **Given** a handler with zero configuration
- **When** the handler writes `<error>`
- **Then** the status is 500 and the body contains no part of `<text>`

##### Examples:
| error | text |
|-------|------|
| `errors.New("pq: connection refused to 10.0.0.5")` | `10.0.0.5` |
| `fmt.Errorf("user alice@example.com: %w", io.ErrClosedPipe)` | `alice@example.com` |
| a typed-nil `*MyError` stored in an `error` | `MyError` |

### Requirement: Mapper results are normalized
A mapper result with a status outside 400–599 SHALL be treated as 500, and the mismatch SHALL be reported to the `OnError` hook. A mapper result without a status SHALL be treated as 500.

#### Scenario: Out-of-range status from a mapper
- **Given** a consumer mapper that returns a problem with status `<returned>` for `ErrX`
- **When** the handler resolves `ErrX`
- **Then** the resolved status is `<status>`
- **And** the hook received an error wrapping `ErrInvalidStatus`: `<reported>`

##### Examples:
| returned | status | reported |
|----------|--------|----------|
| 404 | 404 | no |
| 0 | 500 | no |
| 200 | 500 | yes |
| 302 | 500 | yes |
| 600 | 500 | yes |

### Requirement: Faulty consumer code never breaks resolution
A mapper, describer or enricher that panics SHALL NOT crash the request. A panicking mapper or describer SHALL be treated as no match, a panicking enricher SHALL be skipped. Each panic SHALL be reported to the hooks, whatever the panic value, `nil` included and also when the runtime recovers it as `nil` (`GODEBUG=panicnil=1`). A panicking hook SHALL be ignored, and the hooks after it SHALL still be called.

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
| a mapper that calls `panic(nil)`, registered first | 409 | a panic error with value `*runtime.PanicNilError`, then `ErrX` |

#### Scenario: panic(nil) in consumer code under GODEBUG=panicnil=1
- **Given** a test binary run with `GODEBUG=panicnil=1`, `<faulty>` and a consumer mapper `ErrX` to 409 registered after it
- **When** the handler writes `ErrX`
- **Then** the status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| faulty | status | hook |
|--------|--------|------|
| a mapper that calls `panic(nil)`, registered first | 409 | a panic error with value `nil`, then `ErrX` |
| an enricher that calls `panic(nil)` | 409 | a panic error with value `nil`, then `ErrX` |
| a describer error whose `Problem` method calls `panic(nil)`, wrapping `ErrX` | 409 | a panic error with value `nil`, then `ErrX` |

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

### Requirement: Enrichers
Consumers SHALL be able to register enrichers that run, in order, on every resolved problem before it is written. By default, an enricher SHALL set `instance` to the request path, without the query string, when the problem has no instance.

#### Scenario: Default instance
- **Given** a handler with zero configuration
- **When** `<problem>` is written for request `<request>`
- **Then** the instance is `<instance>`

##### Examples:
| problem | request | instance |
|---------|---------|----------|
| an unmapped error | `GET /v1/vessels/7?token=abc` | `/v1/vessels/7` |
| a problem with instance `urn:uuid:1` | `GET /v1/vessels/7` | `urn:uuid:1` |
| an unmapped error | `GET /` | `/` |

#### Scenario: Custom enricher
- **Given** an enricher that sets extension `trace_id` from the request context
- **When** `<error>` is written
- **Then** the body contains `trace_id`

##### Examples:
| error |
|-------|
| an unmapped error (fallback) |
| a mapped `ErrVesselNotFound` |
| a self-describing problem |

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

### Requirement: Nil errors are not problems
Resolving or writing a nil error SHALL produce no problem, write nothing and not call the hook.

#### Scenario: Nil error
- **Given** a hook that records its calls
- **When** the handler writes `<error>`
- **Then** nothing is written and the hook was called 0 times

##### Examples:
| error |
|-------|
| `nil` |
| `WithStatus(nil, 404)` |

### Requirement: Canceled requests are answered like any other
A canceled request context SHALL NOT stop the library from producing a problem response: the error SHALL be resolved, reported to the hook and answered like any other. A client that is still connected receives the problem; when the client has gone away, the server drops the write.

#### Scenario: Request context canceled
- **Given** a request whose context is `<ctx>`
- **When** the handler writes `<error>`
- **Then** the response is `<response>`
- **And** the hook was called 1 time

##### Examples:
| ctx | error | response |
|-----|-------|----------|
| canceled | `fmt.Errorf("query: %w", context.Canceled)` | a 500 problem |
| active | `context.Canceled` | a 500 problem |
| canceled | `ErrVesselNotFound` | a 404 problem |

### Requirement: Zero-configuration handler and concurrent use
A handler built with no options SHALL work with the built-ins, the default enricher and the fallback. A handler SHALL be safe for concurrent use and SHALL NOT change after construction.

#### Scenario: Zero options
- **Given** a handler created with `<options>`
- **When** the handler writes a `*json.SyntaxError` from `<requests>` concurrent requests
- **Then** every response is a 400 problem

##### Examples:
| options | requests |
|---------|----------|
| no options | 1 |
| no options | 100 |
| a nil option | 1 |

### Requirement: Received problems are never relayed implicitly
A received problem anywhere in the error chain SHALL NOT reach the client: none of its title, detail, type, instance or extension members SHALL appear in the response. With zero configuration it SHALL resolve as `ErrBadGateway` does. Consumer and scoped mappers SHALL take precedence, and the hook SHALL receive the error as returned. A typed-nil received problem in the chain SHALL be skipped like any typed nil, so it does not resolve as `ErrBadGateway`.

#### Scenario: Gateway returns a received problem
- **Given** a handler with `<setup>`
- **And** a received problem `R` holding status 404, detail `vessel 7 missing in db shard-3` and extension `table`=`vessels_shard3`
- **When** a handler returns `<error>`
- **Then** the response status is `<status>`, the type is `<type>` and the detail is `<detail>`
- **And** the body contains the extension `table` is `<leaks>`
- **And** the hook received `<hook>`

##### Examples: safe by default
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| zero configuration | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | `R` |
| zero configuration | `fmt.Errorf("load vessel: %w", R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("%w: %w", prblm.ErrBadGateway, R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("%w: %w", prblm.ErrNotFound, R)` | 404 | `about:blank` | `The requested resource was not found.` | false | the returned error |
| zero configuration | `prblm.WithStatus(R, 503)` | 503 | `about:blank` | (none) | false | the returned error |
| zero configuration | `errors.Join(io.EOF, R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("op: %w", (*prblm.Received)(nil))` | 500 | `about:blank` | (none) | false | the returned error |
| built-ins disabled | `R` | 500 | `about:blank` | (none) | false | `R` |

##### Examples: consumer decides
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| consumer mapper `prblm.As` for `*prblm.Received` returning status 404 with detail `Vessel not found.` | `fmt.Errorf("load: %w", R)` | 404 | `about:blank` | `Vessel not found.` | false | the returned error |
| consumer mapper `prblm.Status(prblm.ErrBadGateway, 503)` | `R` | 503 | `about:blank` | (none) | false | `R` |
| scoped mapper `prblm.Status(prblm.ErrBadGateway, 503)` on the request | `R` | 503 | `about:blank` | (none) | false | `R` |
| consumer mapper `prblm.As` for `*prblm.Received` that panics | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | a panic error, then `R` |
| consumer mapper `prblm.As` for `*prblm.Received` that returns nil | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | `R` |

##### Examples: explicit relay
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| zero configuration | `&R.Problem` | 404 | `about:blank` | `vessel 7 missing in db shard-3` | true | `&R.Problem` |

#### Scenario: Same response in every adapter
- **Given** `<adapter>` with zero configuration
- **And** a received problem `R` holding status 404, detail `vessel 7 missing in db shard-3` and extension `table`=`vessels_shard3`
- **When** a handler returns `fmt.Errorf("%w: %w", prblm.ErrBadGateway, R)`
- **Then** the response is status `<status>`, `Content-Type: application/problem+json` and body `<body>`

##### Examples:
| adapter | status | body |
|---------|--------|------|
| net/http | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |
| gin middleware | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |
| fiber error handler | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |

### Requirement: Error values with panicking methods never break resolution
An error whose own methods panic while the handler resolves it SHALL NOT crash the request; this covers an `Unwrap`, `Is` or `As` method. The panic SHALL abort the walk of the chain, and the answer SHALL be the safe 500 problem, even when another step would have matched. The hooks SHALL receive one `ErrUnwalkableError` note with reason `panic`, wrapping a panic error, then the error. An `As` method reporting a match with an empty pointer or interface target SHALL count as no match, not as a panic.

#### Scenario: Error methods panic
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>` and the body is the problem `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples: zero configuration
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | an error whose `Is` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | a nil pointer to an error type whose `Is` method has a value receiver | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | the error |

##### Examples: the status-hint step
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| built-ins disabled | an error whose `As` method reports a match and leaves its target empty | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | the error |

##### Examples: another step would have matched
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| consumer mapper `prblm.Status(ErrX, 409)` | `errors.Join(ErrX, an error whose As method panics with "boom")` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | `prblm.WithStatus(an error whose Is method panics with "boom", 404)` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | `errors.Join(prblm.New(404), an error whose As method panics with "boom")` | 404 | `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}` | the error |

#### Scenario: Error methods panic without a hook
- **Given** a handler with `<setup>` and no hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** nothing panics out of the handler

##### Examples:
| setup | error | status |
|-------|-------|--------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 |
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 |
| zero configuration | an error whose `Unwrap() error` panics with `"boom"` | 500 |
